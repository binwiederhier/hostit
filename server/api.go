package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// API returns the REST API handler (session cookie or bearer token auth), e.g.
// to mount it on additional listeners
func (s *Server) API() http.Handler {
	return s.api
}

func (s *Server) newAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)

	// Web login (Google OAuth) and session teardown
	mux.HandleFunc("GET /auth/google", s.handleGoogleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleGoogleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)

	// Account: readable while pending, so the web app can explain the wait
	mux.Handle("GET /v1/account", s.authenticated(s.handleAccount))
	mux.Handle("GET /v1/account/keys", s.requireActive(s.handleKeysList))
	mux.Handle("POST /v1/account/keys", s.requireActive(s.handleKeysAdd))
	mux.Handle("DELETE /v1/account/keys/{id}", s.requireActive(s.handleKeysDelete))
	mux.Handle("GET /v1/account/tokens", s.requireActive(s.handleTokensList))
	mux.Handle("POST /v1/account/tokens", s.requireActive(s.handleTokensAdd))
	mux.Handle("DELETE /v1/account/tokens/{id}", s.requireActive(s.handleTokensDelete))

	// Apps: scoped to the caller; admins see and manage everything
	mux.Handle("POST /v1/apps", s.requireActive(s.handleAppsCreate))
	mux.Handle("GET /v1/apps", s.requireActive(s.handleAppsList))
	mux.Handle("GET /v1/apps/{name}", s.requireActive(s.handleAppsGet))
	mux.Handle("DELETE /v1/apps/{name}", s.requireActive(s.handleAppsDelete))
	mux.Handle("PUT /v1/apps/{name}/keys", s.requireActive(s.handleAppsSetKeys))

	// Administration
	mux.Handle("GET /v1/users", s.requireAdmin(s.handleUsersList))
	mux.Handle("PATCH /v1/users/{id}", s.requireAdmin(s.handleUsersUpdate))
	mux.Handle("DELETE /v1/users/{id}", s.requireAdmin(s.handleUsersDelete))
	mux.Handle("GET /v1/settings", s.requireAdmin(s.handleSettingsGet))
	mux.Handle("PATCH /v1/settings", s.requireAdmin(s.handleSettingsUpdate))

	// The web app (single-page, embedded); must be last, it catches the rest
	mux.Handle("/", s.webHandler())
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &apiHealthResponse{Healthy: true})
}

// handleAppsCreate creates an app owned by the caller, enforcing their app limit.
// The app's authorized_keys start as the caller's profile keys plus any keys in
// the request (or a generated key pair if there are none at all).
func (s *Server) handleAppsCreate(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiCreateAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.checkAppLimit(c); err != nil {
		writeAppError(w, err)
		return
	}
	profileKeys, err := s.users.KeyStrings(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	memoryMB, err := s.callerMemoryLimit(c)
	if err != nil {
		writeAppError(w, err)
		return
	}
	opts := &app.CreateOptions{
		OwnerID:     c.userID(),
		RequestKeys: req.SSHKeys,
		ProfileKeys: profileKeys,
		MemoryMB:    memoryMB,
	}
	a, creds, err := s.apps.CreateApp(req.Name, opts)
	if err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App created", "app", a.Name, "port", a.Port, "owner", c.userID())
	writeJSON(w, http.StatusCreated, s.appResponse(a, creds))
}

func (s *Server) handleAppsList(w http.ResponseWriter, _ *http.Request, c *caller) {
	apps, err := s.visibleApps(c)
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiAppResponse, 0, len(apps))
	for _, a := range apps {
		resp = append(resp, s.appResponse(a, nil))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAppsGet(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appResponse(a, nil))
}

func (s *Server) handleAppsDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.apps.DeleteApp(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App deleted", "app", a.Name, "by", c.userID())
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app deleted"})
}

func (s *Server) handleAppsSetKeys(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiSetKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	profileKeys, err := s.users.KeyStrings(a.OwnerID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.apps.SetKeys(a.Name, req.SSHKeys, profileKeys); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appResponse(a, nil))
}

// visibleApps returns the caller's own apps, or all apps for admins
func (s *Server) visibleApps(c *caller) ([]*store.App, error) {
	if c.isAdmin() {
		return s.apps.Apps()
	}
	return s.apps.Store().AppsByOwner(c.user.ID)
}

// ownedApp fetches an app the caller may act on (own app, or any app for admins)
func (s *Server) ownedApp(c *caller, name string) (*store.App, error) {
	a, err := s.apps.App(name)
	if err != nil {
		return nil, err
	}
	if !c.isAdmin() && a.OwnerID != c.userID() {
		return nil, store.ErrAppNotFound // Don't leak the existence of other people's apps
	}
	return a, nil
}

// checkAppLimit rejects app creation once the caller reached their limit; the
// global admin token is unlimited
func (s *Server) checkAppLimit(c *caller) error {
	if c.user == nil {
		return nil
	}
	limits, err := s.users.Limits(c.user)
	if err != nil {
		return err
	}
	count, err := s.apps.Store().AppCountByOwner(c.user.ID)
	if err != nil {
		return err
	}
	if count >= limits.AppLimit {
		return fmt.Errorf("%w: app limit reached (%d of %d), delete an app or ask an admin to raise your limit",
			app.ErrLimitReached, count, limits.AppLimit)
	}
	return nil
}

// callerMemoryLimit returns the memory cap for a new app of this caller
func (s *Server) callerMemoryLimit(c *caller) (int, error) {
	if c.user == nil {
		defaults, err := s.users.Defaults()
		if err != nil {
			return 0, err
		}
		return defaults.MemoryMB, nil
	}
	limits, err := s.users.Limits(c.user)
	if err != nil {
		return 0, err
	}
	return limits.MemoryMB, nil
}

// writeAppError maps app, store and user errors to HTTP status codes
func writeAppError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAppNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, app.ErrAppExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, app.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, app.ErrLimitReached):
		writeError(w, http.StatusForbidden, err)
	default:
		writeUserError(w, err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, &apiErrorResponse{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
