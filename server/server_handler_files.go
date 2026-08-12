package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"

	"heckel.io/hostit/store"
)

// Agent-API file operations: list, read, write, move, mkdir, delete, tar upload
// and README replacement. Thin handlers over app.Manager's file methods.

// handleAgentFileList lists one directory, named by ?path= and defaulting to the
// app's root. It is not the whole tree: an app with dependencies installed would
// otherwise answer with tens of thousands of entries.
func (s *Server) handleAgentFileList(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	listing, err := s.apps.ListFiles(a.Name, r.URL.Query().Get("path"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleAgentFileGet(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	// ?stat=1 returns metadata (size, modtime, MIME) instead of the bytes, so the
	// editor can tell text from binary without downloading the whole file.
	if r.URL.Query().Has("stat") {
		info, err := s.apps.StatFile(a.Name, r.PathValue("path"))
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	b, err := s.apps.ReadFile(a.Name, r.PathValue("path"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Served from the web app's own origin, and an admin may read any user's
	// files: never let one tenant's HTML run here. Downloading is the only thing
	// this endpoint is for.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(r.PathValue("path"))))
	_, _ = w.Write(b)
}

func (s *Server) handleAgentFilePut(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	mode, err := uploadMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Straight from the socket to disk: a body big enough to matter must never
	// be held in the daemon, which shares a small box with every app container
	relPath := r.PathValue("path")
	if err := s.apps.WriteFileFrom(a.Name, relPath, r.Body, mode); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiMessageResponse{Message: "wrote " + relPath})
}

// uploadMode parses an octal ?mode= such as 755, so a binary or script can be
// uploaded ready to run; empty means the default
func uploadMode(raw string) (os.FileMode, error) {
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fmt.Errorf("invalid mode %q: use octal permissions such as 644 or 755", raw)
	}
	return os.FileMode(parsed), nil
}

// handleAgentMove renames or moves a file within the app's home (used by the web
// file browser to drag a file into a folder or rename it).
func (s *Server) handleAgentMove(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiMoveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.From == "" || req.To == "" {
		writeError(w, http.StatusBadRequest, errors.New("both from and to are required"))
		return
	}
	if err := s.apps.MoveFile(a.Name, req.From, req.To); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "moved " + req.From + " to " + req.To})
}

// handleAgentMkdir creates an empty directory in the app's home (used by the web
// file browser's "new folder" button).
func (s *Server) handleAgentMkdir(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiMkdirRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if err := s.apps.MakeDir(a.Name, req.Path); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "created " + req.Path})
}

func (s *Server) handleAgentFileDelete(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	if err := s.apps.DeleteFile(a.Name, r.PathValue("path")); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "deleted " + r.PathValue("path")})
}

func (s *Server) handleAgentFileUpload(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	written, err := s.apps.ExtractTar(a.Name, io.LimitReader(r.Body, maxTarUpload))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiFilesWrittenResponse{Written: written})
}

func (s *Server) handleAgentReadmePut(w http.ResponseWriter, r *http.Request, c *caller, a *store.App) {
	var req apiReadmeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.apps.WriteReadme(a.Name, req.Readme); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "README.md updated"})
}
