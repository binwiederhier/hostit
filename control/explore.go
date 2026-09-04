package control

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/preview"
	"heckel.io/hostit/store"
)

// The public app gallery ("Explore"): any logged-in user can browse the PUBLIC
// apps their owners chose to list. Gated instance-wide by app-listing (config,
// admin-overridable). A listing only ever surfaces a public, live app -- never a
// private, restricted, or soft-deleted one -- and the page itself is behind the
// login, so it is a gallery for an instance's members, not the open internet.

// appListingEnabled reports whether the gallery is on: the admin DB override if
// set, else the control.yml default.
func (s *Server) appListingEnabled() bool {
	if v, err := s.apps.Store().Setting(store.SettingAppListing); err == nil && v != "" {
		return v == "true"
	}
	return s.config.AppListing
}

// handleExplore lists the public, listed apps. It needs a logged-in account (the
// gallery is not exposed anonymously) and returns nothing when the gallery is off.
func (s *Server) handleExplore(w http.ResponseWriter, _ *http.Request, _ *caller) {
	out := make([]*apiExploreApp, 0)
	if !s.appListingEnabled() {
		writeJSON(w, http.StatusOK, &apiExploreResponse{Enabled: false, Apps: out})
		return
	}
	apps, err := s.apps.Store().Apps()
	if err != nil {
		writeAppError(w, err)
		return
	}
	for _, a := range apps {
		if a.Private || !a.Listed || !a.SoftDeletedAt.IsZero() {
			continue
		}
		out = append(out, &apiExploreApp{
			Name:        a.Name,
			URL:         s.apps.URL(a),
			Description: s.apps.Description(a.Name),
			HasShot:     s.previewShotExists(a.ID),
		})
	}
	writeJSON(w, http.StatusOK, &apiExploreResponse{Enabled: true, Apps: out})
}

// previewShotExists reports whether a stored screenshot is on disk for this app,
// so the gallery renders a card image instead of an empty box.
func (s *Server) previewShotExists(appID string) bool {
	if s.config.AppPreview != config.AppPreviewScreenshot {
		return false
	}
	_, err := os.Stat(filepath.Join(preview.Dir(s.config.DataDir), appID+".png"))
	return err == nil
}

// handleExplorePreview serves a listed app's stored screenshot to any logged-in
// user. The per-app preview endpoint is owner-only, and the gallery is exactly
// the case where somebody who does NOT own the app may see its picture -- but
// only for an app that is public, listed, and on a gallery that is switched on.
func (s *Server) handleExplorePreview(w http.ResponseWriter, r *http.Request, _ *caller) {
	if !s.appListingEnabled() {
		writeAppError(w, store.ErrAppNotFound)
		return
	}
	a, err := s.apps.App(r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if a.Private || !a.Listed || !a.SoftDeletedAt.IsZero() {
		writeAppError(w, store.ErrAppNotFound)
		return
	}
	b, err := os.ReadFile(filepath.Join(preview.Dir(s.config.DataDir), a.ID+".png"))
	if errors.Is(err, fs.ErrNotExist) {
		writeAppError(w, store.ErrAppNotFound)
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// handleAppsSetListed toggles whether a PUBLIC app appears on the gallery. Owner
// only. Listing a private app, or listing at all while the gallery is off, is
// refused, so the flag can never claim "public" for an app that is not.
func (s *Server) handleAppsSetListed(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiSetListedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a, err := s.ownerApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if req.Listed {
		if !s.appListingEnabled() {
			writeError(w, http.StatusForbidden, errors.New("the app gallery is disabled on this instance"))
			return
		}
		if a.Private {
			writeError(w, http.StatusBadRequest, errors.New("only a public app can be listed"))
			return
		}
	}
	if err := s.apps.Store().SetAppListed(a.Name, req.Listed); err != nil {
		writeAppError(w, err)
		return
	}
	a.Listed = req.Listed
	s.logAction(c, a.Name, "listed", listedAction(req.Listed))
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{s.appResponseFor(c, a, s.firstActiveDomain(a.Name))})[0])
}

func listedAction(listed bool) string {
	if listed {
		return "App listed on the gallery"
	}
	return "App removed from the gallery"
}
