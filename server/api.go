package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// API returns the admin REST API handler (Bearer-token authenticated), e.g. to
// mount it on additional listeners
func (s *Server) API() http.Handler {
	return s.api
}

func (s *Server) newAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.Handle("POST /v1/apps", s.auth(s.handleAppsCreate))
	mux.Handle("GET /v1/apps", s.auth(s.handleAppsList))
	mux.Handle("GET /v1/apps/{name}", s.auth(s.handleAppsGet))
	mux.Handle("DELETE /v1/apps/{name}", s.auth(s.handleAppsDelete))
	mux.Handle("PUT /v1/apps/{name}/keys", s.auth(s.handleAppsSetKeys))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &apiHealthResponse{Healthy: true})
}

func (s *Server) handleAppsCreate(w http.ResponseWriter, r *http.Request) {
	var req apiCreateAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a, creds, err := s.apps.CreateApp(req.Name, req.SSHKeys)
	if err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App created", "app", a.Name, "port", a.Port)
	writeJSON(w, http.StatusCreated, s.appResponse(a, creds))
}

func (s *Server) handleAppsList(w http.ResponseWriter, _ *http.Request) {
	apps, err := s.apps.Apps()
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiAppResponse, 0)
	for _, a := range apps {
		resp = append(resp, s.appResponse(a, nil))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAppsGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.apps.App(r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appResponse(a, nil))
}

func (s *Server) handleAppsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.apps.DeleteApp(name); err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App deleted", "app", name)
	writeJSON(w, http.StatusOK, &apiHealthResponse{Healthy: true})
}

func (s *Server) handleAppsSetKeys(w http.ResponseWriter, r *http.Request) {
	var req apiSetKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	if err := s.apps.SetKeys(name, req.SSHKeys); err != nil {
		writeAppError(w, err)
		return
	}
	a, err := s.apps.App(name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appResponse(a, nil))
}

// auth wraps a handler with Bearer-token authentication against the admin token
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("invalid or missing bearer token"))
			return
		}
		next(w, r)
	})
}

// writeAppError maps app/store errors to HTTP status codes
func writeAppError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAppNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, app.ErrAppExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, app.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
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
