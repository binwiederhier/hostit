package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/store"
)

// The viewer surface: users who may open a private app's URL and nothing else.
// Deliberately thinner than the collaborator surface next door -- no SSH keys
// ride along, no working access -- because that difference is the entire
// reason the grant exists.

func (s *Server) handleViewersList(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	users, err := s.apps.Store().AppViewers(a.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiCollaboratorResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, &apiCollaboratorResponse{ID: u.ID, Email: u.Email, Name: u.Name})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleViewersAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownerApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiAddCollaboratorRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Only existing, approved accounts: no invite flow, no pending states.
	u, err := s.users.UserByEmail(req.Email)
	if err != nil || u.Status != store.StatusActive {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no active user %q", req.Email))
		return
	}
	if u.ID == a.OwnerID {
		writeError(w, http.StatusBadRequest, errors.New("the owner can already see the app"))
		return
	}
	// A collaborator can already open it, so a viewer grant on top would be a
	// second row that means nothing -- and would reappear in the list as if it
	// were doing something.
	if s.apps.Store().IsAppCollaborator(a.ID, u.ID) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is already a collaborator, which includes access", u.Email))
		return
	}
	if err := s.apps.Store().AddAppViewer(a.ID, u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "viewer-added", "Gave "+u.Email+" access")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "viewer added"})
}

// handleViewersRemove is owner-only, with no "leave" branch of its own. A
// collaborator can remove themselves because they can reach the app's API at
// all; a viewer cannot -- viewing grants no API access, by design -- so the
// branch would be unreachable rather than merely unused.
func (s *Server) handleViewersRemove(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownerApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.apps.Store().RemoveAppViewer(a.ID, r.PathValue("id")); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "viewer-removed", "Removed someone's access")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "viewer removed"})
}
