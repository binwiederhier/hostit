package server

import (
	"log/slog"
	"net/http"
	"time"

	"heckel.io/hostit/store"
)

// recordEvent appends one entry to an app's activity log (the Logs tab). A failure
// is logged, never returned, so auditing can never break the action it records.
func (s *Server) recordEvent(appName, actor, level, action, detail string) {
	err := s.apps.Store().AddEvent(&store.Event{
		AppName:   appName,
		CreatedAt: time.Now(),
		Actor:     actor,
		Level:     level,
		Action:    action,
		Detail:    detail,
	})
	if err != nil {
		slog.Warn("Cannot record app event", "app", appName, "action", action, "error", err)
	}
}

// logAction records a user action, attributed to the caller's email (empty for the
// global admin token).
func (s *Server) logAction(c *caller, appName, action, detail string) {
	s.recordEvent(appName, s.ownerEmail(c.userID()), "info", action, detail)
}

// handleAppEvents returns an app's recent activity log, newest first, for the
// Logs tab.
func (s *Server) handleAppEvents(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	events, err := s.apps.Store().AppEvents(a.Name, 100)
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiEventResponse, 0, len(events))
	for _, e := range events {
		resp = append(resp, &apiEventResponse{
			Time:   e.CreatedAt,
			Actor:  e.Actor,
			Level:  e.Level,
			Action: e.Action,
			Detail: e.Detail,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
