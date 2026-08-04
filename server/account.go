package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

// handleAccount returns the caller's identity, limits and usage; pending users
// may call this (that is how the web app knows to show "waiting for approval")
func (s *Server) handleAccount(w http.ResponseWriter, _ *http.Request, c *caller) {
	resp, err := s.accountResponse(c)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleKeysList(w http.ResponseWriter, _ *http.Request, c *caller) {
	keys, err := s.users.Keys(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleKeysAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiAddKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.user == nil {
		writeError(w, http.StatusBadRequest, errors.New("the global admin token has no profile; use a user account"))
		return
	}
	key, err := s.users.AddKey(c.user.ID, req.Label, req.Key)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.syncUserAppKeys(c.user.ID); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (s *Server) handleKeysDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	if err := s.users.DeleteKey(c.userID(), r.PathValue("id")); err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.syncUserAppKeys(c.userID()); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "key deleted"})
}

// handleTokensList returns the account-wide tokens. App-scoped tokens are
// deliberately left out: they belong to their app's page, and revoking one from
// here would quietly break whatever agent is using it.
func (s *Server) handleTokensList(w http.ResponseWriter, _ *http.Request, c *caller) {
	tokens, err := s.users.Tokens(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	accountTokens := make([]*store.Token, 0, len(tokens))
	for _, tk := range tokens {
		if tk.AppName == "" {
			accountTokens = append(accountTokens, tk)
		}
	}
	writeJSON(w, http.StatusOK, accountTokens)
}

func (s *Server) handleTokensAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiAddTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.user == nil {
		writeError(w, http.StatusBadRequest, errors.New("the global admin token has no profile; use a user account"))
		return
	}
	// An app-scoped token may only be minted for an app the caller owns
	if req.AppName != "" {
		if _, err := s.ownedApp(c, req.AppName); err != nil {
			writeAppError(w, err)
			return
		}
	}
	token, tk, err := s.users.CreateAppToken(c.user.ID, req.AppName, req.Label)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiTokenResponse{
		ID:        tk.ID,
		Prefix:    tk.Prefix,
		Label:     tk.Label,
		AppName:   tk.AppName,
		CreatedAt: tk.CreatedAt,
		Token:     token, // Shown exactly once
	})
}

func (s *Server) handleTokensDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	if err := s.users.DeleteToken(c.userID(), r.PathValue("id")); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "token revoked"})
}

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

// accountResponse assembles identity, limits and current usage for a caller
func (s *Server) accountResponse(c *caller) (*apiAccountResponse, error) {
	if c.globalAdmin {
		return &apiAccountResponse{
			Email:  "admin-token",
			Name:   "Global admin token",
			Role:   store.RoleAdmin,
			Status: store.StatusActive,
		}, nil
	}
	limits, err := s.users.Limits(c.user)
	if err != nil {
		return nil, err
	}
	apps, err := s.apps.Store().AppsByOwner(c.user.ID)
	if err != nil {
		return nil, err
	}
	diskMB := 0
	for _, a := range apps {
		diskMB += a.DiskMB
	}
	return &apiAccountResponse{
		Email:  c.user.Email,
		Name:   c.user.Name,
		Role:   c.user.Role,
		Status: c.user.Status,
		Limits: limits,
		Usage:  &apiUsage{Apps: len(apps), DiskMB: diskMB},
	}, nil
}

// syncUserAppKeys rewrites authorized_keys for all apps a user owns, so profile
// key changes take effect immediately
func (s *Server) syncUserAppKeys(userID string) error {
	if userID == "" {
		return nil
	}
	apps, err := s.apps.Store().AppsByOwner(userID)
	if err != nil {
		return err
	}
	userKeys, err := s.users.KeyStrings(userID)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if err := s.apps.SyncKeys(a.Name, userKeys); err != nil {
			return err
		}
	}
	return nil
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

// writeUserError maps user-package errors to HTTP status codes
func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUserNotFound), errors.Is(err, store.ErrTokenNotFound), errors.Is(err, store.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, user.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, user.ErrNotActive):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
