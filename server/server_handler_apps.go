package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// App CRUD and the caller/limit helpers those handlers rely on. Thin
// orchestration over app.Manager and user.Manager; the router lives in api.go.

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
	diskMB, err := s.callerDiskLimit(c)
	if err != nil {
		writeAppError(w, err)
		return
	}
	opts := &app.CreateOptions{
		OwnerID:     c.userID(),
		RequestKeys: req.SSHKeys,
		ProfileKeys: profileKeys,
		MemoryMB:    memoryMB,
		DiskMB:      diskMB,
	}
	a, err := s.apps.CreateApp(req.Name, opts)
	if err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("App created", "app", a.Name, "port", a.Port, "owner", c.userID())
	s.logAction(c, a.Name, "created", "App created")
	resp := s.appResponse(a, s.firstActiveDomain(a.Name))
	resp.AgentToken = s.agentToken(a) // Created with the app, never a separate step
	writeJSON(w, http.StatusCreated, resp)
}

// handleAppsFork duplicates an owned app into a new one, seeding its home from a
// snapshot of the source's current home. Requires a btrfs host (501 otherwise).
func (s *Server) handleAppsFork(w http.ResponseWriter, r *http.Request, c *caller) {
	source, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiForkAppRequest
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
	diskMB, err := s.callerDiskLimit(c)
	if err != nil {
		writeAppError(w, err)
		return
	}
	opts := &app.CreateOptions{OwnerID: c.userID(), ProfileKeys: profileKeys, MemoryMB: memoryMB, DiskMB: diskMB}
	a, err := s.apps.Fork(source.Name, req.NewName, req.SnapshotID, opts)
	if err != nil {
		writeSnapshotError(w, err) // ErrSnapshotsUnavailable -> 501; the rest fall through
		return
	}
	slog.Info("App forked", "source", source.Name, "app", a.Name, "owner", c.userID())
	s.logAction(c, a.Name, "created", "Forked from "+source.Name)
	resp := s.appResponse(a, s.firstActiveDomain(a.Name))
	resp.AgentToken = s.agentToken(a)
	writeJSON(w, http.StatusCreated, resp)
}

// handleAppsSetDescription updates the app's one-line description in its hostit.yml.
func (s *Server) handleAppsSetDescription(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiSetDescriptionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.apps.SetDescription(a.Name, req.Description); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "description", "Updated the description")
	writeJSON(w, http.StatusOK, s.appResponse(a, s.firstActiveDomain(a.Name))) // appResponse re-reads the description from the file
}

// handleAppsRename changes an app's name. Everything durable keys on the app id,
// so this is cheap: the Unix login is renamed and the store row updated, and
// nothing (home, snapshots, container) moves. The custom-domain routing cache is
// refreshed so a domain follows the app to its new name.
func (s *Server) handleAppsRename(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiRenameAppRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	renamed, err := s.apps.RenameApp(a.Name, req.NewName)
	if err != nil {
		writeAppError(w, err)
		return
	}
	s.reloadDomains() // a custom domain's routing keys on the name; follow it
	s.logAction(c, renamed.Name, "rename", "Renamed from "+a.Name)
	resp := s.appResponse(renamed, s.firstActiveDomain(renamed.Name))
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{resp})[0])
}

func (s *Server) handleAppsList(w http.ResponseWriter, r *http.Request, c *caller) {
	apps, err := s.listedApps(c, r.URL.Query().Get("all") == "true")
	if err != nil {
		writeAppError(w, err)
		return
	}
	// One query for every app's active custom domain, not one lookup per app.
	activeDomains, err := s.apps.Store().ActiveDomains()
	if err != nil {
		activeDomains = nil // fall back to no custom domains rather than failing the list
	}
	resp := make([]*apiAppResponse, 0, len(apps))
	for _, a := range apps {
		resp = append(resp, s.appResponse(a, activeDomains[a.Name]))
	}
	writeJSON(w, http.StatusOK, s.withState(resp))
}

func (s *Server) handleAppsGet(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := s.appResponse(a, s.firstActiveDomain(a.Name))
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
	s.logAction(c, a.Name, "token", "Regenerated the API token")
	resp := s.appResponse(a, s.firstActiveDomain(a.Name))
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
	if s.assistant != nil {
		s.assistant.DropSession(a.Name) // forget the app's live session + transcript
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
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{s.appResponse(a, s.firstActiveDomain(a.Name))})[0])
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

// callerDiskLimit is the disk quota (MB) to apply to an app the caller creates:
// the owner's limit, or the instance default for the global admin token.
func (s *Server) callerDiskLimit(c *caller) (int, error) {
	if c.user == nil {
		defaults, err := s.users.Defaults()
		if err != nil {
			return 0, err
		}
		return defaults.DiskMB, nil
	}
	limits, err := s.users.Limits(c.user)
	if err != nil {
		return 0, err
	}
	return limits.DiskMB, nil
}
