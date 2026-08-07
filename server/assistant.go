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
	// assistantKeepalive is how often the event stream sends a comment so idle
	// connections are not dropped by a proxy
	assistantKeepalive = 20 * time.Second
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

func (o *appOps) Snapshot(name, label string) (string, error) {
	snap, err := o.apps.TakeSnapshot(name, label, false)
	if err != nil {
		return "", err
	}
	return "saved snapshot " + snap.ID, nil
}

func (o *appOps) ListSnapshots(name string) (string, error) {
	snaps, err := o.apps.ListSnapshots(name)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "no snapshots yet", nil
	}
	var b strings.Builder
	for _, s := range snaps {
		kind := "manual"
		if s.Auto {
			kind = "auto"
		}
		fmt.Fprintf(&b, "%s  %s  %s", s.ID, s.CreatedAt.Format("2006-01-02 15:04"), kind)
		if s.Label != "" {
			fmt.Fprintf(&b, "  %q", s.Label)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// handleAssistant starts a turn for an owned app. The run is owned by the server
// (a background goroutine), not by this request, so it keeps going after the
// sender leaves; its events go to every subscriber of the stream endpoint. This
// returns as soon as the turn starts, or 409 if one is already running.
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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, assistantMaxMessage)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if err := s.assistant.Send(a.Name, c.userID(), req.Message); err != nil {
		switch {
		case errors.Is(err, assistant.ErrBusy):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, assistant.ErrTooManyRuns) || errors.Is(err, assistant.ErrRateLimited):
			writeError(w, http.StatusTooManyRequests, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, &apiMessageResponse{Message: "started"})
}

// handleAssistantStop cancels the app's in-progress assistant turn. The run's
// goroutine unwinds and publishes a done on the stream, so every watcher clears
// its working state; the transcript keeps whatever steps it had saved.
func (s *Server) handleAssistantStop(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.assistant == nil {
		writeError(w, http.StatusNotImplemented, errors.New("the built-in assistant is not configured on this server"))
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	msg := "nothing running"
	if s.assistant.Stop(a.Name) {
		msg = "stopping"
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// handleAssistantStream is the live event feed for an app's assistant, as SSE.
// Every watcher (browser, phone) subscribes here and sees the same stream, so a
// run started on one device shows up on all of them.
func (s *Server) handleAssistantStream(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.assistant == nil {
		writeError(w, http.StatusNotImplemented, errors.New("the built-in assistant is not configured on this server"))
		return
	}
	// Defense in depth: a same-origin gate on the stream too. It is already safe
	// from cross-site reads (the browser blocks them without CORS, which we never
	// send), but refusing a cross-site connection up front avoids even subscribing.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeError(w, http.StatusForbidden, errors.New("cross-site stream refused"))
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	events, cancel, err := s.assistant.Subscribe(a.Name)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, err)
		return
	}
	defer cancel()
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
	keepalive := time.NewTicker(assistantKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return // dropped (too slow); the client reconnects and reloads
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n") // SSE comment; keeps proxies from idling us out
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleAssistantTranscript returns an owned app's stored conversation plus
// whether a turn is running, so a reload or another device shows the history and
// resumes the live state rather than a blank chat.
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
	writeJSON(w, http.StatusOK, &apiAssistantTranscript{Enabled: true, Items: items, Running: s.assistant.Running(a.Name)})
}

// apiAssistantTranscript is GET /api/apps/{name}/assistant
type apiAssistantTranscript struct {
	Enabled bool             `json:"enabled"`
	Running bool             `json:"running"`
	Items   []assistant.Item `json:"items"`
}

func secondsToDuration(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}
