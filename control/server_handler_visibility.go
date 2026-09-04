package control

import (
	"encoding/json"
	"net/http"
)

// handleAppsSetVisibility publishes an app to the world, or takes it back.
//
// It is an OWNERSHIP act, not a collaborator one: a collaborator can already
// deploy to the app and read its files, but deciding who else may see it is
// the owner's call. The routing table follows on its own -- RouteLoop
// re-derives it every half second and pushes when the hash moves -- so there
// is no separate "publish" step to forget.
func (s *Server) handleAppsSetVisibility(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiSetVisibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a, err := s.ownerApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if err := s.apps.Store().SetAppPrivate(a.Name, req.Private); err != nil {
		writeAppError(w, err)
		return
	}
	a.Private = req.Private
	// "Publicly listed" is a rung of the same ladder: a private app is never
	// listed, and neither is anything when the gallery is off, so coerce rather
	// than reject -- the picker only offers "listed" when it is valid anyway.
	listed := req.Listed && !req.Private && s.appListingEnabled()
	if err := s.apps.Store().SetAppListed(a.Name, listed); err != nil {
		writeAppError(w, err)
		return
	}
	a.Listed = listed
	s.logAction(c, a.Name, "visibility", visibilityAction(req.Private))
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{s.appResponseFor(c, a, s.firstActiveDomain(a.Name))})[0])
}

func visibilityAction(private bool) string {
	if private {
		return "App made private"
	}
	return "App made public"
}
