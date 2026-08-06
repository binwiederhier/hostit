package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

// appTranscripts persists the assistant's per-app conversation in the registry as
// one JSON blob, adapting the SQLite store to assistant.Store.
type appTranscripts struct {
	store *store.Store
}

var _ assistant.Store = (*appTranscripts)(nil)

func (t *appTranscripts) Load(app string) ([]assistant.Message, error) {
	blob, err := t.store.LoadAssistantSession(app)
	if err != nil || blob == "" {
		return nil, err
	}
	var messages []assistant.Message
	if err := json.Unmarshal([]byte(blob), &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (t *appTranscripts) Save(app string, messages []assistant.Message) error {
	blob, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return t.store.SaveAssistantSession(app, string(blob))
}

func (t *appTranscripts) Delete(app string) error {
	return t.store.DeleteAssistantSession(app)
}

const (
	// assistantMaxMessage caps a single user prompt to the assistant
	assistantMaxMessage = 32 * 1024
	// assistantReadCap bounds a file the assistant reads, so one huge file cannot
	// blow up the model's context
	assistantReadCap = 128 * 1024
)

// appOps adapts app.Manager to assistant.AppOps: it turns the assistant's generic
// tool calls into the app manager's operations, scoped to one app. This is the
// only place the assistant package meets the app package.
type appOps struct {
	apps *app.Manager
}

var _ assistant.AppOps = (*appOps)(nil)

func (o *appOps) ListFiles(name, path string) (string, error) {
	listing, err := o.apps.ListFiles(name, path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", strings.TrimSuffix(listing.Path, "/"))
	for _, f := range listing.Files {
		if f.Type == app.FileTypeDir {
			fmt.Fprintf(&b, "  %s/\n", f.Path)
		} else {
			fmt.Fprintf(&b, "  %s (%d bytes)\n", f.Path, f.Size)
		}
	}
	if listing.Truncated {
		b.WriteString("  ... (truncated)\n")
	}
	return b.String(), nil
}

func (o *appOps) ReadFile(name, path string) (string, error) {
	b, err := o.apps.ReadFile(name, path)
	if err != nil {
		return "", err
	}
	if len(b) > assistantReadCap {
		return string(b[:assistantReadCap]) + "\n... (truncated)", nil
	}
	return string(b), nil
}

func (o *appOps) WriteFile(name, path, content string) error {
	return o.apps.WriteFile(name, path, []byte(content), 0)
}

func (o *appOps) Exec(name, command string, timeoutSeconds int) (assistant.ExecResult, error) {
	res, err := o.apps.Exec(name, command, secondsToDuration(timeoutSeconds))
	if err != nil {
		return assistant.ExecResult{}, err
	}
	return assistant.ExecResult{
		Output:    res.Output,
		ExitCode:  res.ExitCode,
		Truncated: res.Truncated,
		TimedOut:  res.TimedOut,
	}, nil
}

func (o *appOps) Logs(name string, lines int) (string, error) {
	return o.apps.Logs(name, lines)
}

func (o *appOps) Deploy(name string) (string, error) {
	return o.apps.Up(name)
}

// handleAssistant runs one assistant turn for an owned app and streams the loop
// -- thinking, text, each tool call and its result -- to the browser as SSE, so a
// phone watching the build sees it happen live.
func (s *Server) handleAssistant(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.assistant == nil {
		writeError(w, http.StatusNotImplemented, errors.New("the built-in assistant is not configured on this server"))
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req struct {
		Message string `json:"message"`
		Reset   bool   `json:"reset"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, assistantMaxMessage)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if req.Reset {
		if err := s.assistant.Reset(a.Name); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// emit serializes one event to the client. Called from Run's goroutine in
	// order, so no locking is needed.
	emit := func(ev assistant.Event) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// Run already reports its own failure as a final error event; nothing else to
	// do with the returned error here.
	_ = s.assistant.Run(r.Context(), a.Name, req.Message, emit)
}

// handleAssistantTranscript returns an owned app's stored conversation, so a
// reload or another device shows the history rather than a blank chat.
func (s *Server) handleAssistantTranscript(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.assistant == nil {
		writeJSON(w, http.StatusOK, &apiAssistantTranscript{Enabled: false})
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	items, err := s.assistant.Transcript(a.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiAssistantTranscript{Enabled: true, Items: items})
}

// apiAssistantTranscript is GET /api/apps/{name}/assistant
type apiAssistantTranscript struct {
	Enabled bool             `json:"enabled"`
	Items   []assistant.Item `json:"items"`
}

func secondsToDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}
