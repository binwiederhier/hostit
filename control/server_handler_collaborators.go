package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/store"
)

// The collaborator surface: users the owner grants full working access to an
// app (everything but the ownership acts). Their profile SSH keys ride along
// on the app's managed authorized_keys while the grant holds.

func (s *Server) handleCollaboratorsList(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	users, err := s.apps.Store().AppCollaborators(a.ID)
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

func (s *Server) handleCollaboratorsAdd(w http.ResponseWriter, r *http.Request, c *caller) {
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
	u, err := s.grantableUser(req.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if u.ID == a.OwnerID {
		writeError(w, http.StatusBadRequest, errors.New("the owner already has full access"))
		return
	}
	if err := s.apps.Store().AddAppCollaborator(a.ID, u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	// Collaborating includes looking, so a viewer row underneath it would say
	// nothing and would show up twice in the set the proxy enforces on.
	if err := s.apps.Store().RemoveAppViewer(a.ID, u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	// Their profile keys join the app's managed authorized_keys immediately.
	if err := s.resyncAppKeys(a); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "collaborator-added", "Added collaborator "+u.Email)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "collaborator added"})
}

func (s *Server) handleCollaboratorsRemove(w http.ResponseWriter, r *http.Request, c *caller) {
	// The owner (or an admin) removes anyone; a collaborator may remove
	// THEMSELVES (leave). Everyone else is forbidden.
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	userID := r.PathValue("id")
	if !c.isAdmin() && a.OwnerID != c.userID() && userID != c.userID() {
		writeError(w, http.StatusForbidden, errors.New("only the app's owner may remove other collaborators"))
		return
	}
	if err := s.apps.Store().RemoveAppCollaborator(a.ID, userID); err != nil {
		writeAppError(w, err)
		return
	}
	// Their profile keys leave with the grant.
	if err := s.resyncAppKeys(a); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "collaborator-removed", "Removed a collaborator")
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "collaborator removed"})
}

// appProfileKeys is every profile key with standing access to the app: the
// owner's plus each collaborator's. App-specific keys are stored separately
// and merged by the app manager.
func (s *Server) appProfileKeys(a *store.App) ([]string, error) {
	keys, err := s.users.KeyStrings(a.OwnerID)
	if err != nil {
		return nil, err
	}
	collaborators, err := s.apps.Store().AppCollaborators(a.ID)
	if err != nil {
		return nil, err
	}
	for _, u := range collaborators {
		userKeys, err := s.users.KeyStrings(u.ID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, userKeys...)
	}
	return keys, nil
}

// resyncAppKeys rewrites the app's authorized_keys from its stored app keys
// plus the full profile-key set (owner + collaborators).
// resyncAppKeys rewrites an app's authorized_keys after an access change. The
// app's OWN keys are registry state, so control resolves them here and hands
// the node the complete set: a node cannot be asked for them (app_key is not
// in the pushed mirror, so a split node would answer with an empty list and
// drop the owner's keys).
func (s *Server) resyncAppKeys(a *store.App) error {
	profileKeys, err := s.appProfileKeys(a)
	if err != nil {
		return err
	}
	appKeys, err := s.apps.Store().AppKeys(a.Name)
	if err != nil {
		return err
	}
	if err := s.node.SetKeys(a.Name, appKeys, s.apps.appendRelayKey(a.Host, profileKeys)); err != nil {
		return err
	}
	s.apps.refreshSSHRelay() // keep the relay frontend's authorized_keys current (prompt revocation)
	return nil
}

// handleAppsTransfer moves the app to a new owner (an existing, approved
// user). The old owner stays on as a collaborator, so a transfer never locks
// anyone out of their own work; if the recipient was a collaborator, that
// grant dissolves into ownership. Ownership counts against the recipient's
// app limit from then on, so a full account is refused.
func (s *Server) handleAppsTransfer(w http.ResponseWriter, r *http.Request, c *caller) {
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
	u, err := s.users.UserByEmail(req.Email)
	if err != nil || u.Status != store.StatusActive {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no active user %q", req.Email))
		return
	}
	if u.ID == a.OwnerID {
		writeError(w, http.StatusBadRequest, errors.New("that user already owns this app"))
		return
	}
	limits, err := s.users.Limits(u)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if limits.AppLimit > 0 {
		count, err := s.apps.Store().AppCountByOwner(u.ID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		if count >= limits.AppLimit {
			writeError(w, http.StatusForbidden, fmt.Errorf("%q is at their app limit", req.Email))
			return
		}
	}
	oldOwner := a.OwnerID
	if err := s.apps.Store().SetAppOwner(a.Name, u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	_ = s.apps.Store().RemoveAppCollaborator(a.ID, u.ID) // ownership absorbs the grant
	if oldOwner != "" {
		_ = s.apps.Store().AddAppCollaborator(a.ID, oldOwner)
	}
	a.OwnerID = u.ID
	if err := s.resyncAppKeys(a); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "transferred", "Transferred ownership to "+u.Email)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "ownership transferred"})
}
