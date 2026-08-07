package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// apiSnapshotResponse is one snapshot in the API
type apiSnapshotResponse struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Auto      bool      `json:"auto"`
}

// apiTakeSnapshotRequest is the body of POST /apps/{app}/snapshots
type apiTakeSnapshotRequest struct {
	Label string `json:"label"`
}

func snapshotView(s *store.Snapshot) *apiSnapshotResponse {
	return &apiSnapshotResponse{ID: s.ID, Label: s.Label, CreatedAt: s.CreatedAt, Auto: s.Auto}
}

// handleAgentSnapshotList returns an app's snapshots, newest first.
func (s *Server) handleAgentSnapshotList(w http.ResponseWriter, _ *http.Request, _ *caller, a *store.App) {
	snaps, err := s.apps.ListSnapshots(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := make([]*apiSnapshotResponse, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, snapshotView(snap))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAgentSnapshotTake takes a manual snapshot (optionally labelled), so an
// agent can preserve important work before a risky change.
func (s *Server) handleAgentSnapshotTake(w http.ResponseWriter, r *http.Request, _ *caller, a *store.App) {
	var req apiTakeSnapshotRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	}
	snap, err := s.apps.TakeSnapshot(a.Name, req.Label, false)
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshotView(snap))
}

// handleAgentRestore rolls an app back to a snapshot (taking a safety snapshot of
// the current state first).
func (s *Server) handleAgentRestore(w http.ResponseWriter, r *http.Request, _ *caller, a *store.App) {
	id := r.PathValue("id")
	if err := s.apps.Rollback(a.Name, id); err != nil {
		writeSnapshotError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "rolled back to " + id})
}

// writeSnapshotError maps snapshot errors to status codes: unavailable (not btrfs)
// is 501, an unknown id is 404, the rest fall through.
func writeSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrSnapshotsUnavailable):
		writeError(w, http.StatusNotImplemented, err)
	case errors.Is(err, store.ErrSnapshotNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		writeAppError(w, err)
	}
}
