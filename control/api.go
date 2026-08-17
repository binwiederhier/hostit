package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"

	"heckel.io/hostit/appctl"
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
	mux.HandleFunc("POST /auth/breakglass", s.handleBreakglass)
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
	route(mux, "PUT", "/apps/{name}/description", s.requireActive(s.handleAppsSetDescription))
	route(mux, "POST", "/apps/{name}/rename", s.requireActive(s.handleAppsRename))
	route(mux, "POST", "/apps/{name}/fork", s.requireActive(s.handleAppsFork))
	route(mux, "GET", "/apps/{name}/collaborators", s.requireActive(s.handleCollaboratorsList))
	route(mux, "POST", "/apps/{name}/collaborators", s.requireActive(s.handleCollaboratorsAdd))
	route(mux, "DELETE", "/apps/{name}/collaborators/{id}", s.requireActive(s.handleCollaboratorsRemove))
	route(mux, "POST", "/apps/{name}/transfer", s.requireActive(s.handleAppsTransfer))
	route(mux, "GET", "/apps/{name}/preview.png", s.requireActive(s.handleAppsPreview))
	route(mux, "POST", "/apps/{name}/preview", s.requireActive(s.handleAppsPreviewRefresh))
	route(mux, "GET", "/apps/{name}/events", s.requireActive(s.handleAppEvents))
	route(mux, "GET", "/apps/{name}/domains", s.requireActive(s.handleAppDomainsList))
	route(mux, "POST", "/apps/{name}/domains", s.requireActive(s.handleAppDomainAdd))
	route(mux, "POST", "/apps/{name}/domains/{domain}/verify", s.requireActive(s.handleAppDomainVerify))
	route(mux, "DELETE", "/apps/{name}/domains/{domain}", s.requireActive(s.handleAppDomainDelete))
	route(mux, "GET", "/apps/{name}/terminal", s.requireActive(s.handleTerminal))
	route(mux, "GET", "/apps/{name}/assistant", s.requireActive(s.handleAssistantTranscript))
	route(mux, "GET", "/apps/{name}/assistant/stream", s.requireActive(s.handleAssistantStream))
	route(mux, "POST", "/apps/{name}/assistant", s.requireActive(s.handleAssistant))
	route(mux, "POST", "/apps/{name}/assistant/upload", s.requireActive(s.handleAssistantUpload))
	route(mux, "DELETE", "/apps/{name}/assistant/upload", s.requireActive(s.handleAssistantUploadDelete))
	route(mux, "POST", "/apps/{name}/assistant/stop", s.requireActive(s.handleAssistantStop))

	// Administration
	route(mux, "GET", "/users", s.requireAdmin(s.handleUsersList))
	route(mux, "POST", "/users", s.requireAdmin(s.handleUsersInvite))
	route(mux, "PATCH", "/users/{id}", s.requireAdmin(s.handleUsersUpdate))
	route(mux, "DELETE", "/users/{id}", s.requireAdmin(s.handleUsersDelete))
	route(mux, "GET", "/domains", s.requireAdmin(s.handleDomainsList))
	route(mux, "POST", "/domains", s.requireAdmin(s.handleDomainsAdd))
	route(mux, "DELETE", "/domains/{domain}", s.requireAdmin(s.handleDomainsDelete))
	route(mux, "GET", "/cluster", s.requireAdmin(s.handleClusterStatus))
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

// writeAppError maps app, store and user errors to HTTP status codes
func writeAppError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, appctl.ErrPoweredOff):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrAppNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrAppExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, ErrLimitReached):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, ErrInvalidDomain):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, store.ErrAppDomainExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrAppDomainNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, fs.ErrNotExist):
		// A file or directory the caller named is not there (the web editor
		// probes hostit.yml and README.md eagerly): their 404, not our 500.
		writeError(w, http.StatusNotFound, err)
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
