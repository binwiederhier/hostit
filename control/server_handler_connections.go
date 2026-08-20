package control

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/store"
)

// The HTTP surface for connections: the owner connects in their profile, grants
// per app, and an app asks for a token over its own socket.

// connectStatePrefix marks an OAuth state as belonging to a connection rather
// than a login. Both come back on /auth/callback in this PoC, so the callback
// has to tell them apart before it does anything else.
const connectStatePrefix = "conn:"

// apiConnection is one connected account as the UI sees it. It never carries
// the credential -- not even masked, since a mask still says how long it is.
type apiConnection struct {
	Provider  string              `json:"provider"`
	Label     string              `json:"label"`
	Kind      string              `json:"kind"`
	Connected bool                `json:"connected"`
	Available bool                `json:"available"`
	Scopes    string              `json:"scopes,omitempty"`
	Meta      string              `json:"meta,omitempty"`
	Help      string              `json:"help,omitempty"`
	Fields    []connections.Field `json:"fields,omitempty"`
}

// requireUser refuses a caller with no user account behind it. An admin token
// authenticates as the INSTANCE, not a person, and a connection belongs to a
// person: without this the handlers reach into a nil user and take the daemon
// with them.
func (s *Server) requireUser(w http.ResponseWriter, c *caller) bool {
	if c.user == nil {
		writeError(w, http.StatusForbidden, errors.New("connections belong to a user account; sign in or use an account token"))
		return false
	}
	return true
}

// handleConnectionsList is every provider this instance knows, with whether the
// caller has connected it. Providers that cannot be offered here are still
// listed but marked unavailable, so the page can say why rather than hide them.
func (s *Server) handleConnectionsList(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	if !s.requireUser(w, c) {
		return
	}
	existing := map[string]*store.Connection{}
	if list, err := s.apps.Store().Connections(c.user.ID); err == nil {
		for _, conn := range list {
			existing[conn.Provider] = conn
		}
	}
	out := make([]apiConnection, 0)
	for _, p := range connections.All() {
		item := apiConnection{
			Provider:  p.Name,
			Label:     p.Label,
			Kind:      p.Kind,
			Available: s.connections.available(p),
			Help:      p.Help,
			Fields:    p.Fields,
		}
		if conn, ok := existing[p.Name]; ok {
			item.Connected = true
			item.Scopes = conn.Scopes
			item.Meta = conn.Meta
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleConnectionStart begins an OAuth connection: it answers with the URL to
// send the owner to. The state is stored in the same cookie the login uses and
// prefixed, so the shared callback can route it.
func (s *Server) handleConnectionStart(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireUser(w, c) {
		return
	}
	p, ok := s.availableProvider(w, r.PathValue("provider"))
	if !ok {
		return
	}
	if p.Kind != connections.KindOAuth {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is connected by pasting a credential, not a consent flow", p.Label))
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state := connectStatePrefix + p.Name + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), state, int(oauthTimeout.Seconds()*40)))
	writeJSON(w, http.StatusOK, &apiMessageResponse{
		Message: p.AuthCodeURL(s.config.GoogleClientID, s.config.RedirectURL(hostOnly(r.Host)), state),
	})
}

// handleConnectionSave stores a static provider's pasted credential.
func (s *Server) handleConnectionSave(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireUser(w, c) {
		return
	}
	p, ok := s.availableProvider(w, r.PathValue("provider"))
	if !ok {
		return
	}
	if p.Kind != connections.KindStatic {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is connected through a consent flow", p.Label))
		return
	}
	var values map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&values); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.connections.saveStatic(c.user.ID, p, values); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.logAction(c, "", "connection", "Connected "+p.Label)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "connected " + p.Label})
}

// handleConnectionDelete disconnects a provider, taking its grants with it.
func (s *Server) handleConnectionDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireUser(w, c) {
		return
	}
	provider := r.PathValue("provider")
	if err := s.apps.Store().DeleteConnection(c.user.ID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logAction(c, "", "connection", "Disconnected "+provider)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "disconnected"})
}

// handleAppConnectionsList is what this app has been granted, and what it could
// be. Discovery informs; it never auto-grants.
func (s *Server) handleAppConnectionsList(w http.ResponseWriter, r *http.Request, c *caller) {
	if !s.requireUser(w, c) {
		return
	}
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	granted, err := s.apps.Store().AppConnections(a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	available, _ := s.apps.Store().Connections(c.user.ID)
	out := make([]apiConnection, 0, len(available))
	for _, conn := range available {
		p, ok := connections.Lookup(conn.Provider)
		if !ok {
			continue
		}
		out = append(out, apiConnection{
			Provider: p.Name, Label: p.Label, Kind: p.Kind,
			Connected: contains(granted, p.Name), Available: true, Meta: conn.Meta,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppConnectionGrant and ...Revoke are the per-app half of the model.
func (s *Server) handleAppConnectionGrant(w http.ResponseWriter, r *http.Request, c *caller) {
	s.setAppGrant(w, r, c, true)
}

func (s *Server) handleAppConnectionRevoke(w http.ResponseWriter, r *http.Request, c *caller) {
	s.setAppGrant(w, r, c, false)
}

func (s *Server) setAppGrant(w http.ResponseWriter, r *http.Request, c *caller, grant bool) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	provider := r.PathValue("provider")
	// Only what the OWNER has actually connected can be granted: a grant naming
	// nothing would be a promise the token endpoint cannot keep.
	if _, err := s.apps.Store().Connection(a.OwnerID, provider); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is not connected", provider))
		return
	}
	if grant {
		err = s.apps.Store().GrantConnection(a.ID, provider)
	} else {
		err = s.apps.Store().RevokeConnection(a.ID, provider)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	verb := "Granted "
	if !grant {
		verb = "Revoked "
	}
	s.logAction(c, a.Name, "connection", verb+provider)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "ok"})
}

// handleSelfConnectionToken is what an app calls over its own socket. It is the
// whole point of the feature: a usable credential, minted per request, never
// written to disk or baked into an environment variable that cannot be
// refreshed.
func (s *Server) handleSelfConnectionToken(w http.ResponseWriter, r *http.Request, a *store.App) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	token, err := s.connections.tokenFor(r.Context(), a, r.PathValue("provider"))
	switch {
	case errors.Is(err, errNotGranted):
		// 403 not 404: the connection may well exist, and the fix is in the
		// app's settings rather than the owner's profile.
		writeError(w, http.StatusForbidden, err)
		return
	case errors.Is(err, errNotConnected):
		writeError(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

// handleSelfConnectionsList lets an app discover what it was granted, so it can
// say "connect Google" rather than failing at the first API call.
func (s *Server) handleSelfConnectionsList(w http.ResponseWriter, r *http.Request, a *store.App) {
	granted, err := s.apps.Store().AppConnections(a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if granted == nil {
		granted = []string{}
	}
	writeJSON(w, http.StatusOK, granted)
}

// availableProvider resolves a provider and refuses one this instance cannot
// offer, writing the error itself.
func (s *Server) availableProvider(w http.ResponseWriter, name string) (connections.Provider, bool) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return connections.Provider{}, false
	}
	p, ok := connections.Lookup(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown connection %q", name))
		return connections.Provider{}, false
	}
	if !s.connections.available(p) {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("%s is not configured on this server", p.Label))
		return connections.Provider{}, false
	}
	return p, true
}

// connectionFromState completes a consent flow that came back on the shared
// login callback. It returns false when the state is an ordinary login, which
// is what keeps the two flows apart.
func (s *Server) connectionFromState(w http.ResponseWriter, r *http.Request, state string) bool {
	if !strings.HasPrefix(state, connectStatePrefix) {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(state, connectStatePrefix), ":", 2)
	provider := parts[0]
	c, err := s.authenticate(r)
	if err != nil || c.user == nil {
		// The owner must already be signed in: a connection belongs to an
		// account, and there is no account here to attach it to.
		writeError(w, http.StatusUnauthorized, errors.New("sign in before connecting an account"))
		return true
	}
	p, ok := s.availableProvider(w, provider)
	if !ok {
		return true
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("missing authorization code"))
		return true
	}
	if err := s.connections.saveOAuth(r.Context(), c.user.ID, p, code, s.config.RedirectURL(hostOnly(r.Host))); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return true
	}
	s.logAction(c, "", "connection", "Connected "+p.Label)
	http.Redirect(w, r, "/profile?connected="+p.Name, http.StatusSeeOther)
	return true
}
