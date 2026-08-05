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

const (
	// apiPrefix is where the API lives: /api/apps/{app}/... for one app's own
	// endpoints, /api/account and /api/users for everything else. The split it
	// replaced (/api/{app} versus /v1) said nothing about which token reached
	// what, which is the only distinction that matters here.
	apiPrefix = "/api"
)

var (
	// ErrForbidden is returned when a caller asks for something their role does
	// not cover, e.g. a non-admin asking to see everyone's apps
	ErrForbidden = errors.New("administrator access required")
)

// API returns the REST API handler (session cookie or bearer token auth), e.g.
// to mount it on additional listeners
func (s *Server) API() http.Handler {
	return s.api
}

// route registers one API endpoint under the single prefix everything shares
func route(mux *http.ServeMux, method, path string, h http.Handler) {
	mux.Handle(method+" "+apiPrefix+path, h)
}

func (s *Server) newAPIHandler() http.Handler {
	mux := http.NewServeMux()
	route(mux, "GET", "/health", http.HandlerFunc(s.handleHealth))

	// Web login (Google OAuth) and session teardown
	mux.HandleFunc("GET /auth/google", s.handleGoogleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleGoogleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)

	// Account: readable while pending, so the web app can explain the wait
	route(mux, "GET", "/account", s.authenticated(s.handleAccount))
	route(mux, "GET", "/account/keys", s.requireActive(s.handleKeysList))
	route(mux, "POST", "/account/keys", s.requireActive(s.handleKeysAdd))
	route(mux, "DELETE", "/account/keys/{id}", s.requireActive(s.handleKeysDelete))
	route(mux, "GET", "/account/tokens", s.requireActive(s.handleTokensList))
	route(mux, "POST", "/account/tokens", s.requireActive(s.handleTokensAdd))
	route(mux, "DELETE", "/account/tokens/{id}", s.requireActive(s.handleTokensDelete))

	// Apps: scoped to the caller; admins see and manage everything
	route(mux, "POST", "/apps", s.requireActive(s.handleAppsCreate))
	route(mux, "GET", "/apps", s.requireActive(s.handleAppsList))
	route(mux, "GET", "/apps/{name}", s.requireActive(s.handleAppsGet))
	route(mux, "DELETE", "/apps/{name}", s.requireActive(s.handleAppsDelete))
	route(mux, "PUT", "/apps/{name}/keys", s.requireActive(s.handleAppsSetKeys))
	route(mux, "POST", "/apps/{name}/token", s.requireActive(s.handleAppsRotateToken))
	route(mux, "GET", "/apps/{name}/terminal", s.requireActive(s.handleTerminal))

	// Administration
	route(mux, "GET", "/users", s.requireAdmin(s.handleUsersList))
	route(mux, "POST", "/users", s.requireAdmin(s.handleUsersInvite))
	route(mux, "PATCH", "/users/{id}", s.requireAdmin(s.handleUsersUpdate))
	route(mux, "DELETE", "/users/{id}", s.requireAdmin(s.handleUsersDelete))
	route(mux, "GET", "/domains", s.requireAdmin(s.handleDomainsList))
	route(mux, "POST", "/domains", s.requireAdmin(s.handleDomainsAdd))
	route(mux, "DELETE", "/domains/{domain}", s.requireAdmin(s.handleDomainsDelete))
	route(mux, "GET", "/settings", s.requireAdmin(s.handleSettingsGet))
	route(mux, "PATCH", "/settings", s.requireAdmin(s.handleSettingsUpdate))

	// Agent-facing API (see agentapi.go)
	s.newAgentRoutes(mux)

	// Anything else under the API prefix is a mistake, and an agent deserves to
	// hear it as JSON rather than receive the web app's index.html with a 200.
	// This also covers the paths the API used to have.
	mux.HandleFunc(apiPrefix+"/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such endpoint: %s %s (see %s/info)", r.Method, r.URL.Path, apiPrefix))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, fmt.Errorf("the API moved to %s; %s is gone", apiPrefix, r.URL.Path))
	})

	// The web app (single-page, embedded); must be last, it catches the rest
	mux.Handle("/", s.webHandler())
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &apiHealthResponse{Healthy: true})
}

// handleAppsCreate creates an app owned by the caller, enforcing their app limit.
// The app's authorized_keys start as the caller's profile keys plus any keys in
// the request; with neither, the app is reachable through the API only.
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
	a, err := s.apps.CreateApp(req.Name, opts)
	if err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App created", "app", a.Name, "port", a.Port, "owner", c.userID())
	resp := s.appResponse(a)
	resp.AgentToken = s.agentToken(a) // Created with the app, never a separate step
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleAppsList(w http.ResponseWriter, r *http.Request, c *caller) {
	apps, err := s.listedApps(c, r.URL.Query().Get("all") == "true")
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiAppResponse, 0, len(apps))
	for _, a := range apps {
		resp = append(resp, s.appResponse(a))
	}
	writeJSON(w, http.StatusOK, s.withState(resp))
}

func (s *Server) handleAppsGet(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := s.appResponse(a)
	resp.AgentToken = s.agentToken(a)
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{resp})[0])
}

// handleAppsRotateToken issues a fresh agent token, invalidating the old one
func (s *Server) handleAppsRotateToken(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	token, err := s.users.RotateAppToken(a.OwnerID, a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := s.appResponse(a)
	resp.AgentToken = token
	// Through withState like every other app response, or rotating a token would
	// hand the web app an app with no live state and flip its status dot to stopped.
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{resp})[0])
}

// agentToken returns the app's agent token, creating it if the app predates
// automatic creation; failures are not fatal, the page just shows no token
func (s *Server) agentToken(a *store.App) string {
	token, err := s.users.AppToken(a.OwnerID, a.Name)
	if err != nil {
		slog.Warn("Cannot read agent token", "app", a.Name, "error", err)
		return ""
	}
	return token
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
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{s.appResponse(a)})[0])
}

// listedApps returns the caller's own apps, or every app when an admin asks for
// them. Being an admin does not silently widen the list: the dashboard is a
// personal view, with the caller's own app count printed next to it, so another
// user's app appearing there is a bug rather than a privilege.
func (s *Server) listedApps(c *caller, all bool) ([]*store.App, error) {
	if !all {
		if c.user == nil {
			return s.apps.Apps() // The global admin token owns nothing, so "own" means all
		}
		return s.apps.Store().AppsByOwner(c.user.ID)
	}
	if !c.isAdmin() {
		return nil, ErrForbidden
	}
	return s.apps.Apps()
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
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, err)
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
