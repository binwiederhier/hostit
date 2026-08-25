package control

import (
	"encoding/json"
	"errors"
	"net/http"

	"heckel.io/hostit/node"
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
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

// handleKeysRename changes a key's label. The key itself is untouched, so
// nothing that trusts it has to be updated.
func (s *Server) handleKeysRename(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiRenameKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.users.RenameKey(c.userID(), r.PathValue("id"), req.Label); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "key renamed"})
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

// handleTokensList returns the account-wide tokens only. Every app has its own
// token, created with it and shown on its page; listing those here too would add
// a row per app and give each credential two homes.
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
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

// accountResponse assembles identity, limits and current usage for a caller
func (s *Server) accountResponse(c *caller) (*apiAccountResponse, error) {
	if c.globalAdmin {
		return &apiAccountResponse{
			Email:   "admin-token",
			Name:    "Global admin token",
			Role:    store.RoleAdmin,
			Status:  store.StatusActive,
			Version: node.Version,
		}, nil
	}
	if c.user == nil {
		return nil, errors.New("this token has no account behind it")
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
	poolMemory, poolDisk, err := s.poolReserved(c.user.ID, "")
	if err != nil {
		return nil, err
	}
	attention := 0
	if conns, err := s.apps.Store().Connections(c.user.ID); err == nil {
		for _, cc := range conns {
			if cc.Status == store.ConnectionStatusNeedsReconnect {
				attention++
			}
		}
	}
	return &apiAccountResponse{
		Email:                    c.user.Email,
		Name:                     c.user.Name,
		Role:                     c.user.Role,
		Status:                   c.user.Status,
		ConnectionsNeedReconnect: attention,
		Limits:                   limits,
		Usage:                    &apiUsage{Apps: len(apps), DiskMB: diskMB, PoolMemoryMB: poolMemory, PoolDiskMB: poolDisk},
		Version:                  node.Version,
	}, nil
}

// syncUserAppKeys rewrites authorized_keys for every app a user has standing
// access to -- owned AND collaborated -- so profile key changes take effect
// immediately everywhere. Each app gets its full profile-key set (owner plus
// all collaborators), never just this user's keys.
func (s *Server) syncUserAppKeys(userID string) error {
	if userID == "" {
		return nil
	}
	owned, err := s.apps.Store().AppsByOwner(userID)
	if err != nil {
		return err
	}
	shared, err := s.apps.Store().AppsByCollaborator(userID)
	if err != nil {
		return err
	}
	for _, a := range append(owned, shared...) {
		if err := s.resyncAppKeys(a); err != nil {
			return err
		}
	}
	return nil
}

// writeUserError maps user-package errors to HTTP status codes
func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUserNotFound), errors.Is(err, store.ErrTokenNotFound),
		errors.Is(err, store.ErrKeyNotFound), errors.Is(err, store.ErrDomainNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, user.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, user.ErrNotActive):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
