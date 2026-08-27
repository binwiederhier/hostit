package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"heckel.io/hostit/store"
)

// The viewer surface: users who may open a private app's URL and nothing else.
// Deliberately thinner than the collaborator surface next door -- no SSH keys
// ride along, no working access -- because that difference is the entire
// reason the grant exists.

// handleKnownViewers lists the distinct emails the caller has granted view
// access to across their own apps, so the new-app dialog can offer them as
// picks rather than making the owner retype an address they have used before.
func (s *Server) handleKnownViewers(w http.ResponseWriter, r *http.Request, c *caller) {
	emails, err := s.apps.Store().ViewerEmailsForOwner(c.userID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"emails": emails})
}

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
	// Pending invites (emails without an account yet) ride in the same list,
	// marked pending. Their "id" is the email, so removing one round-trips through
	// the same DELETE .../viewers/{id} the real viewers use.
	pending, err := s.apps.Store().PendingViewers(a.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	for _, email := range pending {
		resp = append(resp, &apiCollaboratorResponse{ID: email, Email: email, Pending: true})
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
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, errors.New("that does not look like an email address"))
		return
	}
	// No account yet: record a pending invite that becomes a real grant when they
	// first sign in, rather than refusing. This is the only way to share with
	// someone before they have logged in once.
	if _, err := s.users.UserByEmail(email); err != nil {
		if err := s.apps.Store().AddPendingViewer(a.ID, email); err != nil {
			writeAppError(w, err)
			return
		}
		s.logAction(c, a.Name, "viewer-invited", "Invited "+email+" (gets access when they sign in)")
		writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "invited " + email + " -- they get access the first time they sign in"})
		return
	}
	u, err := s.grantableUser(email)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	// A pending invite carries its email as its id (see handleViewersList); a real
	// viewer carries a user id, which never contains "@". So the "@" tells them
	// apart without a second endpoint.
	id := r.PathValue("id")
	if strings.Contains(id, "@") {
		if err := s.apps.Store().RemovePendingViewer(a.ID, id); err != nil {
			writeAppError(w, err)
			return
		}
	} else if err := s.apps.Store().RemoveAppViewer(a.ID, id); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "viewer-removed", "Removed someone's access")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "viewer removed"})
}

// grantableUser resolves an email to an account that may receive a grant, and
// otherwise says exactly why it cannot. There is no invite flow -- the person
// has to have signed in at least once -- and "no active user" left the owner
// guessing which half of that was missing.
func (s *Server) grantableUser(email string) (*store.User, error) {
	u, err := s.users.UserByEmail(email)
	if err != nil {
		return nil, fmt.Errorf("%s has not signed in to hostit yet; they need to sign in once before you can give them access", email)
	}
	switch u.Status {
	case store.StatusActive:
		return u, nil
	case store.StatusPending:
		return nil, fmt.Errorf("%s is signed up but still waiting for an administrator to approve their account", email)
	default:
		return nil, fmt.Errorf("%s's account is suspended", email)
	}
}
