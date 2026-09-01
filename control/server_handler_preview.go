package control

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/preview"
	"heckel.io/hostit/store"
)

// handleAppsPreview serves the app's stored screenshot, written by the preview
// sweep loop (app-preview: screenshot). In any other mode the endpoint does not
// exist, and neither does a shot the sweep has not taken yet.
func (s *Server) handleAppsPreview(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if s.config.AppPreview != config.AppPreviewScreenshot {
		writeAppError(w, store.ErrAppNotFound)
		return
	}
	b, err := os.ReadFile(filepath.Join(preview.Dir(s.config.DataDir), a.ID+".png"))
	if errors.Is(err, fs.ErrNotExist) {
		writeAppError(w, store.ErrAppNotFound)
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// The sweep replaces the file in place; never let the browser pin an old shot
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// handleAppsPreviewRefresh queues a fresh screenshot of the app now (the
// dashboard's manual refresh button). It is a no-op fire-and-forget: the queue
// takes it from here, so the response is an immediate 202.
func (s *Server) handleAppsPreviewRefresh(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if s.previews == nil {
		writeAppError(w, store.ErrAppNotFound) // Not in screenshot mode: no such thing to refresh
		return
	}
	s.previews.Refresh(a.Name)
	w.WriteHeader(http.StatusAccepted)
}
