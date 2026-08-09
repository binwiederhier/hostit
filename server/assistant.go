package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/assistant"
)

const (
	// assistantMaxMessage caps a single user prompt to the assistant
	assistantMaxMessage = 32 * 1024
	// assistantKeepalive is how often the event stream sends a comment so idle
	// connections are not dropped by a proxy
	assistantKeepalive = 20 * time.Second

	// assistantUploadMax bounds one upload request (the whole multipart body)
	assistantUploadMax = 10 << 20 // 10 MB
	// maxAttachmentBytes caps an image read back to attach to a chat message, so a
	// large file an owner staged in their app cannot exhaust the shared daemon's RAM
	maxAttachmentBytes = 6 << 20 // 6 MB (Anthropic's per-image limit is ~5 MB)
)

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
		Message     string          `json:"message"`
		Attachments []apiAttachment `json:"attachments"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, assistantMaxMessage)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(req.Attachments) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("a message or an attachment is required"))
		return
	}
	// Attachments are already saved in the app's home; load image bytes so the model
	// can see them (other files are referenced by path only). Only chat-uploaded
	// files (under uploads/) may be attached, and images are read with a size cap so
	// a large file the owner staged cannot blow up memory on the shared daemon.
	attachments := make([]assistant.Attachment, 0, len(req.Attachments))
	for _, at := range req.Attachments {
		if !strings.HasPrefix(at.Path, "uploads/") {
			continue
		}
		att := assistant.Attachment{Path: at.Path, MediaType: at.MediaType}
		if strings.HasPrefix(at.MediaType, "image/") {
			if b, err := s.apps.ReadFileMax(a.Name, at.Path, maxAttachmentBytes); err == nil {
				att.Data = base64.StdEncoding.EncodeToString(b)
			}
		}
		attachments = append(attachments, att)
	}
	if err := s.assistant.Send(a.Name, c.userID(), req.Message, attachments...); err != nil {
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

// apiAttachment is one uploaded file: saved in the app at Path, with its media
// type. The chat send echoes these back to attach them to a message.
type apiAttachment struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Name      string `json:"name,omitempty"`
	IsImage   bool   `json:"is_image,omitempty"`
}

// handleAssistantUpload saves uploaded files into the app's uploads/ folder and
// returns their in-app paths, so the next chat message can attach them. The files
// land in the app's home where the assistant's tools can use them; images are also
// shown to the model when attached (see handleAssistant).
func (s *Server) handleAssistantUpload(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Cap the whole body (ParseMultipartForm's argument is only the in-memory
	// buffer; without this the rest spills to host temp files unbounded).
	r.Body = http.MaxBytesReader(w, r.Body, assistantUploadMax)
	if err := r.ParseMultipartForm(assistantUploadMax); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll() // clean up any spilled temp files
		}
	}()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("no files uploaded"))
		return
	}
	out := make([]apiAttachment, 0, len(files))
	used := map[string]bool{} // names taken in this request, to avoid clobbering
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, assistantUploadMax))
		_ = f.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name := s.uniqueUploadName(a.Name, sanitizeUploadName(fh.Filename), used)
		path := "uploads/" + name
		if err := s.apps.WriteFile(a.Name, path, data, 0); err != nil {
			writeAppError(w, err)
			return
		}
		mediaType := fh.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = mime.TypeByExtension(filepath.Ext(name))
		}
		out = append(out, apiAttachment{
			Path:      path,
			MediaType: mediaType,
			Name:      name,
			IsImage:   strings.HasPrefix(mediaType, "image/"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAssistantUploadDelete removes an uploaded file, used when the owner drops
// an attachment before sending it (so it does not orphan in uploads/). Scoped to
// the uploads/ folder; sent attachments are the app's files and are not touched here.
func (s *Server) handleAssistantUploadDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	if !strings.HasPrefix(path, "uploads/") {
		writeError(w, http.StatusBadRequest, errors.New("only files under uploads/ can be removed here"))
		return
	}
	if err := s.apps.DeleteFile(a.Name, path); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "removed"})
}

// uniqueUploadName finds a free name under uploads/ for base, appending -1, -2, ...
// before the extension when the name is already taken in this request or on disk, so
// two files with the same name do not clobber each other (or an existing file).
func (s *Server) uniqueUploadName(app, base string, used map[string]bool) string {
	free := func(n string) bool { return !used[n] && !s.apps.FileExists(app, "uploads/"+n) }
	if free(base) {
		used[base] = true
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if free(cand) {
			used[cand] = true
			return cand
		}
	}
	used[base] = true
	return base
}

// sanitizeUploadName reduces an uploaded filename to a safe basename (no path, only
// simple characters), so it cannot escape the uploads/ folder.
func sanitizeUploadName(name string) string {
	base := filepath.Base(filepath.Clean("/" + name))
	if base == "/" || base == "." || base == ".." || base == "" {
		return "file"
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, base)
	if base == "" {
		return "file"
	}
	return base
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
