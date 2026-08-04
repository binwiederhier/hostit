// Administration: users, the two ways in that skip the approval queue, and the
// global default limits. Everything here sits behind requireAdmin (see api.go);
// the caller's own account surface is in account.go.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/store"
)

func (s *Server) handleUsersList(w http.ResponseWriter, _ *http.Request, _ *caller) {
	users, err := s.users.Users()
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiUserResponse, 0, len(users))
	for _, u := range users {
		count, err := s.apps.Store().AppCountByOwner(u.ID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		resp = append(resp, newUserResponse(u, count))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUsersInvite creates an approved account for someone who has not signed
// in yet, so an admin can hand out access directly instead of waiting for a
// request to approve
func (s *Server) handleUsersInvite(w http.ResponseWriter, r *http.Request, _ *caller) {
	var req apiInviteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Role == "" {
		req.Role = store.RoleUser
	}
	u, err := s.users.Invite(req.Email, req.Role)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newUserResponse(u, 0))
}

// handleDomainsList returns the email domains that skip the approval queue
func (s *Server) handleDomainsList(w http.ResponseWriter, _ *http.Request, _ *caller) {
	domains, err := s.users.AllowedDomains()
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiDomainResponse, 0, len(domains))
	for _, d := range domains {
		resp = append(resp, &apiDomainResponse{Domain: d.Domain, CreatedAt: d.CreatedAt})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDomainsAdd allows a whole email domain to sign up without approval
func (s *Server) handleDomainsAdd(w http.ResponseWriter, r *http.Request, _ *caller) {
	var req apiAddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d, err := s.users.AllowDomain(req.Domain)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiDomainResponse{Domain: d.Domain, CreatedAt: d.CreatedAt})
}

// handleDomainsDelete stops auto-approving a domain; accounts already approved
// under it are untouched
func (s *Server) handleDomainsDelete(w http.ResponseWriter, r *http.Request, _ *caller) {
	if err := s.users.DisallowDomain(r.PathValue("domain")); err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "domain removed"})
}

// handleUsersUpdate changes role, status and per-user limit overrides; a null
// limit means "fall back to the global default"
func (s *Server) handleUsersUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiUpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.users.User(r.PathValue("id"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if req.Role != nil {
		if *req.Role != store.RoleAdmin && *req.Role != store.RoleUser {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid role %q", *req.Role))
			return
		}
		if u.ID == c.userID() && *req.Role != store.RoleAdmin {
			writeError(w, http.StatusBadRequest, errors.New("you cannot remove your own admin role"))
			return
		}
		u.Role = *req.Role
	}
	if req.Status != nil {
		switch *req.Status {
		case store.StatusPending, store.StatusActive, store.StatusDenied:
			u.Status = *req.Status
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid status %q", *req.Status))
			return
		}
	}
	if req.AppLimitSet {
		u.AppLimit = req.AppLimit
	}
	if req.MemoryMBSet {
		u.MemoryMB = req.MemoryMB
	}
	if req.DiskMBSet {
		u.DiskMB = req.DiskMB
	}
	if err := s.users.Update(u); err != nil {
		writeAppError(w, err)
		return
	}
	count, err := s.apps.Store().AppCountByOwner(u.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(u, count))
}

// handleUsersDelete removes a user and all of their apps (including the Unix
// users and their containers)
func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	id := r.PathValue("id")
	if id == c.userID() {
		writeError(w, http.StatusBadRequest, errors.New("you cannot delete your own account"))
		return
	}
	u, err := s.users.User(id)
	if err != nil {
		writeAppError(w, err)
		return
	}
	apps, err := s.apps.Store().AppsByOwner(u.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	for _, a := range apps {
		if err := s.apps.DeleteApp(a.Name); err != nil {
			writeAppError(w, err)
			return
		}
	}
	if err := s.users.Delete(u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "user deleted"})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request, _ *caller) {
	defaults, err := s.users.Defaults()
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiSettingsResponse{
		DefaultAppLimit: defaults.AppLimit,
		DefaultMemoryMB: defaults.MemoryMB,
		DefaultDiskMB:   defaults.DiskMB,
	})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request, _ *caller) {
	var req apiUpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defaults, err := s.users.Defaults()
	if err != nil {
		writeAppError(w, err)
		return
	}
	if req.DefaultAppLimit != nil {
		defaults.AppLimit = *req.DefaultAppLimit
	}
	if req.DefaultMemoryMB != nil {
		defaults.MemoryMB = *req.DefaultMemoryMB
	}
	if req.DefaultDiskMB != nil {
		defaults.DiskMB = *req.DefaultDiskMB
	}
	if err := s.users.SetDefaults(defaults); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiSettingsResponse{
		DefaultAppLimit: defaults.AppLimit,
		DefaultMemoryMB: defaults.MemoryMB,
		DefaultDiskMB:   defaults.DiskMB,
	})
}

func newUserResponse(u *store.User, appCount int) *apiUserResponse {
	return &apiUserResponse{
		ID:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Role:      u.Role,
		Status:    u.Status,
		AppLimit:  u.AppLimit,
		MemoryMB:  u.MemoryMB,
		DiskMB:    u.DiskMB,
		AppCount:  appCount,
		CreatedAt: u.CreatedAt,
	}
}
