package control

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/mcp"
	"heckel.io/hostit/store"
)

const (
	// connectStatePrefix marks an OAuth state as belonging to a connection
	// rather than a login. Both come back to the same callback, so connecting an
	// account needs no second redirect URI registered with the provider.
	connectStatePrefix = "conn:"
	// maxConnectionsPerUser bounds how many an owner can attach. Generous, but
	// not unbounded: each one is a credential hostit is responsible for.
	maxConnectionsPerUser = 50
)

var (
	// ErrInvalidSlug is returned for a name an app could not address.
	ErrInvalidSlug = errors.New("invalid name")
	// slugRegex: lowercase, digits and dashes, starting and ending on a
	// character. It goes in a URL path an app builds by hand, so it stays boring.
	slugRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,30}[a-z0-9])$`)
)

// apiConnectionResponse is one attached connection. The secret is never in it.
type apiConnectionResponse struct {
	Slug          string    `json:"slug"`
	Label         string    `json:"label,omitempty"`
	Provider      string    `json:"provider"`
	ProviderLabel string    `json:"provider_label"`
	Kind          string    `json:"kind"`
	Meta          string    `json:"meta,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	GrantedApps   int       `json:"granted_apps"`
	// URL and Tools are set for an MCP connection only. They come out of the
	// meta column, which for MCP holds a JSON document rather than the "k=v"
	// context a pasted credential carries -- so it is unpacked here instead of
	// handed to the UI raw.
	URL   string       `json:"url,omitempty"`
	Tools []apiMCPTool `json:"tools,omitempty"`
}

// apiProviderResponse is one thing this instance can connect, for the UI to
// offer. Providers with no client configured are not in the list at all.
type apiProviderResponse struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Help  string `json:"help,omitempty"`
	// NameHint is what the add dialog suggests calling it, which varies by
	// provider and is not something the form can guess.
	NameHint string              `json:"name_hint,omitempty"`
	Fields   []connections.Field `json:"fields,omitempty"`
}

type apiConnectionsResponse struct {
	Connections []*apiConnectionResponse `json:"connections"`
	Providers   []apiProviderResponse    `json:"providers"`
}

// apiAddConnectionRequest is the body of POST /api/connections. For a static
// provider Values carries the pasted fields; for an OAuth one the response
// carries a redirect_url the browser follows.
type apiAddConnectionRequest struct {
	Provider string            `json:"provider"`
	Slug     string            `json:"slug"`
	Label    string            `json:"label,omitempty"`
	Values   map[string]string `json:"values,omitempty"`
}

// apiUpdateConnectionRequest renames a connection, replaces a pasted
// credential, or both.
type apiUpdateConnectionRequest struct {
	Slug   string            `json:"slug,omitempty"`
	Label  string            `json:"label,omitempty"`
	Values map[string]string `json:"values,omitempty"`
}

type apiConnectStartedResponse struct {
	RedirectURL string `json:"redirect_url"`
}

type apiAppConnectionsResponse struct {
	Granted   []*apiConnectionResponse `json:"granted"`
	Available []*apiConnectionResponse `json:"available"`
}

func (s *Server) connectionView(c *store.Connection) *apiConnectionResponse {
	label := c.Provider
	if p, ok := connections.Lookup(c.Provider); ok {
		label = p.Label
	}
	n, _ := s.apps.Store().CountGrants(c.ID)
	out := &apiConnectionResponse{
		Slug: c.Slug, Label: c.Label, Provider: c.Provider, ProviderLabel: label,
		Kind: c.Kind, Meta: c.Meta, CreatedAt: c.CreatedAt, GrantedApps: n,
	}
	if c.Kind == store.ConnectionMCP {
		out.Meta = "" // it is a JSON document, not something to show as-is
		if meta, err := decodeMCPMeta(c.Meta); err == nil {
			out.URL, out.Tools = meta.URL, mcpToolViews(meta.Tools)
		}
	}
	return out
}

// handleConnectionsList returns the caller's connections and what this instance
// can connect.
func (s *Server) handleConnectionsList(w http.ResponseWriter, r *http.Request, c *caller) {
	list, err := s.apps.Store().Connections(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := &apiConnectionsResponse{Connections: make([]*apiConnectionResponse, 0, len(list)), Providers: s.offeredProviders()}
	for _, conn := range list {
		out.Connections = append(out.Connections, s.connectionView(conn))
	}
	writeJSON(w, http.StatusOK, out)
}

// offeredProviders is what this instance can actually connect right now.
func (s *Server) offeredProviders() []apiProviderResponse {
	out := make([]apiProviderResponse, 0)
	if s.connections == nil {
		return out
	}
	for _, p := range s.connections.offered() {
		out = append(out, apiProviderResponse{Name: p.Name, Label: p.Label, Kind: p.Kind, Help: p.Help, NameHint: p.NameHint, Fields: p.Fields})
	}
	return out
}

// handleConnectionAdd attaches a connection. A pasted credential is stored
// immediately; an OAuth one answers with where to send the browser.
func (s *Server) handleConnectionAdd(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	var req apiAddConnectionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if !slugRegex.MatchString(slug) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%w: use lowercase letters, digits and dashes, 3-32 characters", ErrInvalidSlug))
		return
	}
	p, ok := connections.Lookup(req.Provider)
	if !ok || !s.connections.available(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q cannot be connected on this server", req.Provider))
		return
	}
	existing, err := s.apps.Store().Connections(c.userID())
	if err != nil {
		writeAppError(w, err)
		return
	}
	if len(existing) >= maxConnectionsPerUser {
		writeError(w, http.StatusForbidden, fmt.Errorf("you already have %d connections", len(existing)))
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = p.Label
	}
	if p.Kind == connections.KindMCP {
		conn, redirect, err := s.connections.addMCP(r.Context(), c.userID(), slug, label, req.Values["url"])
		var consent errMCPNeedsConsent
		if errors.As(err, &consent) {
			// The server wants authorization: send the owner to consent rather
			// than storing a connection that cannot do anything yet.
			redirect, err = s.startMCPConsent(w, r, c.userID(), slug, label, strings.TrimSpace(req.Values["url"]), consent.discovery)
			if err != nil {
				writeConnectionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, &apiConnectStartedResponse{RedirectURL: redirect})
			return
		}
		if err != nil {
			writeConnectionError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s.connectionView(conn))
		return
	}
	if p.Kind == connections.KindStatic {
		conn, err := s.connections.saveStatic(c.userID(), slug, label, p, req.Values)
		if err != nil {
			writeConnectionError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s.connectionView(conn))
		return
	}
	// OAuth: the slug is carried through the consent round trip in the state,
	// so the callback knows what to call the connection it is about to create.
	url, err := s.startConsent(w, r, p, slug, label)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiConnectStartedResponse{RedirectURL: url})
}

// handleConnectionUpdate renames a connection or replaces its credential.
func (s *Server) handleConnectionUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	conn, err := s.ownedConnection(c, r.PathValue("slug"))
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	var req apiUpdateConnectionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Values) > 0 {
		p, ok := connections.Lookup(conn.Provider)
		if !ok || p.Kind != connections.KindStatic {
			writeError(w, http.StatusBadRequest, errors.New("this connection's credential is replaced by reconnecting, not by editing"))
			return
		}
		if err := s.connections.updateStatic(conn, p, req.Values); err != nil {
			writeConnectionError(w, err)
			return
		}
	}
	if req.Slug != "" || req.Label != "" {
		slug, label := conn.Slug, conn.Label
		if req.Slug != "" {
			slug = strings.ToLower(strings.TrimSpace(req.Slug))
			if !slugRegex.MatchString(slug) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%w: use lowercase letters, digits and dashes, 3-32 characters", ErrInvalidSlug))
				return
			}
		}
		if req.Label != "" {
			label = strings.TrimSpace(req.Label)
		}
		if err := s.apps.Store().RenameConnection(conn.ID, slug, label); err != nil {
			writeConnectionError(w, err)
			return
		}
		conn.Slug, conn.Label = slug, label
	}
	writeJSON(w, http.StatusOK, s.connectionView(conn))
}

// handleConnectionReconnect starts a fresh consent for an existing OAuth
// connection, keeping its slug and every grant that names it.
func (s *Server) handleConnectionReconnect(w http.ResponseWriter, r *http.Request, c *caller) {
	conn, err := s.ownedConnection(c, r.PathValue("slug"))
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	p, ok := connections.Lookup(conn.Provider)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("this connection is not an OAuth account"))
		return
	}
	// An MCP server is re-authorized against ITS OWN authorization server, which
	// is discovered rather than configured, so it cannot go through startConsent.
	if p.Kind == connections.KindMCP {
		meta, err := decodeMCPMeta(conn.Meta)
		if err != nil {
			writeConnectionError(w, err)
			return
		}
		disco, err := mcp.Discover(r.Context(), s.connections.client, meta.URL)
		if err != nil || !disco.CanAuthorize {
			writeError(w, http.StatusBadGateway, fmt.Errorf("that server no longer offers a way to authorize"))
			return
		}
		url, err := s.startMCPConsent(w, r, conn.UserID, conn.Slug, conn.Label, meta.URL, disco)
		if err != nil {
			writeConnectionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, &apiConnectStartedResponse{RedirectURL: url})
		return
	}
	if p.Kind != connections.KindOAuth {
		writeError(w, http.StatusBadRequest, errors.New("this connection is not an OAuth account"))
		return
	}
	url, err := s.startConsent(w, r, p, conn.Slug, conn.Label)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiConnectStartedResponse{RedirectURL: url})
}

// handleConnectionDelete forgets a connection and every grant naming it.
func (s *Server) handleConnectionDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	conn, err := s.ownedConnection(c, r.PathValue("slug"))
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	if err := s.apps.Store().DeleteConnection(conn.ID); err != nil {
		writeConnectionError(w, err)
		return
	}
	// Removing it means removing it: the live access token minted from this
	// credential must not sit in memory for the rest of its hour.
	s.connections.expireCachedFor(conn.ID)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "disconnected"})
}

// handleAppConnectionsList shows what an app holds and what it could be given.
func (s *Server) handleAppConnectionsList(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	granted, err := s.apps.Store().AppConnections(a.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Offered from the APP OWNER's connections, not the caller's: an admin
	// looking at someone else's app must not be shown their own to grant.
	all, err := s.apps.Store().Connections(a.OwnerID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := &apiAppConnectionsResponse{Granted: make([]*apiConnectionResponse, 0), Available: make([]*apiConnectionResponse, 0)}
	held := make(map[string]bool, len(granted))
	for _, conn := range granted {
		held[conn.ID] = true
		out.Granted = append(out.Granted, s.connectionView(conn))
	}
	for _, conn := range all {
		if !held[conn.ID] {
			out.Available = append(out.Available, s.connectionView(conn))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppConnectionGrant lets one app use one of the owner's connections.
func (s *Server) handleAppConnectionGrant(w http.ResponseWriter, r *http.Request, c *caller) {
	a, conn, err := s.appAndOwnedConnection(c, r)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	if err := s.apps.Store().GrantConnection(a.ID, conn.ID); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "connection", "Granted "+conn.Slug)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "granted"})
}

// handleAppConnectionRevoke takes the grant away; the connection stays.
func (s *Server) handleAppConnectionRevoke(w http.ResponseWriter, r *http.Request, c *caller) {
	a, conn, err := s.appAndOwnedConnection(c, r)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	if err := s.apps.Store().RevokeConnection(a.ID, conn.ID); err != nil {
		writeAppError(w, err)
		return
	}
	s.logAction(c, a.Name, "connection", "Revoked "+conn.Slug)
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "revoked"})
}

// appAndOwnedConnection resolves both halves of a grant, refusing a connection
// that is not the app owner's.
func (s *Server) appAndOwnedConnection(c *caller, r *http.Request) (*store.App, *store.Connection, error) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		return nil, nil, err
	}
	conn, err := s.apps.Store().ConnectionBySlug(a.OwnerID, r.PathValue("slug"))
	if err != nil {
		return nil, nil, err
	}
	// A collaborator may manage the app but not spend somebody else's
	// credential: only the owner grants their own connections.
	if conn.UserID != c.userID() {
		return nil, nil, store.ErrConnectionNotFound
	}
	return a, conn, nil
}

// ownedConnection resolves one of the caller's own connections by slug.
func (s *Server) ownedConnection(c *caller, slug string) (*store.Connection, error) {
	return s.apps.Store().ConnectionBySlug(c.userID(), strings.ToLower(strings.TrimSpace(slug)))
}

// startConsent builds the provider's consent URL and remembers, in the state
// cookie, which connection is being made.
func (s *Server) startConsent(w http.ResponseWriter, r *http.Request, p connections.Provider, slug, label string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	state := connectStatePrefix + p.Name + ":" + slug + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), state, int((30*time.Minute).Seconds())))
	// The label is remembered separately: it is free text and has no business
	// in an OAuth state parameter the provider echoes back.
	http.SetCookie(w, s.cookie(s.cookieName(connectLabelCookieName), label, int((30*time.Minute).Seconds())))
	clientID, _ := s.connections.clientFor(p.Name)
	return p.AuthCodeURL(clientID, s.config.RedirectURL(hostOnly(r.Host)), state), nil
}

// connectionFromState handles the OAuth callback when it belongs to a
// connection rather than a login. Reports whether it took the request.
func (s *Server) connectionFromState(w http.ResponseWriter, r *http.Request, state string) bool {
	if !strings.HasPrefix(state, connectStatePrefix) || s.connections == nil {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(state, connectStatePrefix), ":", 3)
	if len(parts) != 3 {
		writeError(w, http.StatusBadRequest, errors.New("invalid connection state"))
		return true
	}
	providerName, slug := parts[0], parts[1]
	// The consent came back through the browser, so the session cookie is the
	// only thing identifying who is connecting.
	who, err := s.authenticate(r)
	if err != nil || who.user == nil {
		writeError(w, http.StatusUnauthorized, errors.New("please sign in and try again"))
		return true
	}
	user := who.user
	// MCP finishes elsewhere: its authorization server was discovered, and the
	// PKCE verifier that redeems the code is held in memory under the nonce.
	if providerName == connections.ProviderMCP {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Redirect(w, r, "/connections", http.StatusFound)
			return true
		}
		s.finishMCPConsent(w, r, user.ID, parts[2], code)
		return true
	}
	p, ok := connections.Lookup(providerName)
	if !ok || !s.connections.available(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q cannot be connected here", providerName))
		return true
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		// The owner declined at the provider, which is not an error worth a
		// stack trace -- send them back to where they started.
		http.Redirect(w, r, "/profile", http.StatusFound)
		return true
	}
	label := p.Label
	if ck, err := r.Cookie(s.cookieName(connectLabelCookieName)); err == nil && ck.Value != "" {
		label = ck.Value
	}
	http.SetCookie(w, s.cookie(s.cookieName(connectLabelCookieName), "", -1))
	redirectURL := s.config.RedirectURL(hostOnly(r.Host))

	// An existing slug means this is a re-consent: keep the connection and its
	// grants, swap the credential underneath.
	if existing, err := s.apps.Store().ConnectionBySlug(user.ID, slug); err == nil {
		if err := s.connections.reconnect(r.Context(), existing, p, code, redirectURL); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return true
		}
	} else if _, err := s.connections.saveOAuth(r.Context(), user.ID, slug, label, p, code, redirectURL); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return true
	}
	http.Redirect(w, r, "/profile", http.StatusFound)
	return true
}

// writeConnectionError maps the connection errors onto status codes.
func writeConnectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConnectionNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrConnectionSlugExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, ErrInvalidSlug), errors.Is(err, connections.ErrInvalidCredential):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeAppError(w, err)
	}
}

// apiRotateKeyResponse says how much was re-sealed.
type apiRotateKeyResponse struct {
	Message     string `json:"message"`
	Connections int    `json:"connections"`
}

// handleConnectionsRotateKey re-seals every stored credential under a fresh key.
// It runs in the LIVE process on purpose: a separate one would rewrite the
// database while this one carried on holding the old key in memory.
func (s *Server) handleConnectionsRotateKey(w http.ResponseWriter, r *http.Request, c *caller) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	n, err := s.connections.RotateKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiRotateKeyResponse{
		Message:     "credential key rotated; the previous key is kept as connections.key.previous until you delete it",
		Connections: n,
	})
}

// ---- The app-facing half, over the app's own unix socket ------------------

// apiSelfConnectionResponse is one connection as the APP sees it. No secret, no
// owner: just the name to ask for and enough to know what it is.
type apiSelfConnectionResponse struct {
	Slug          string `json:"slug"`
	Label         string `json:"label,omitempty"`
	Provider      string `json:"provider"`
	ProviderLabel string `json:"provider_label"`
	Kind          string `json:"kind"`
	Meta          string `json:"meta,omitempty"`
}

// handleSelfConnectionsList tells an app which connections it was granted, so
// an agent building the app can discover them rather than being told.
func (s *Server) handleSelfConnectionsList(w http.ResponseWriter, r *http.Request, a *store.App) {
	granted, err := s.apps.Store().AppConnections(a.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := make([]*apiSelfConnectionResponse, 0, len(granted))
	for _, c := range granted {
		label := c.Provider
		if p, ok := connections.Lookup(c.Provider); ok {
			label = p.Label
		}
		out = append(out, &apiSelfConnectionResponse{
			Slug: c.Slug, Label: c.Label, Provider: c.Provider,
			ProviderLabel: label, Kind: c.Kind, Meta: c.Meta,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSelfConnectionToken hands the app a usable credential for one slug.
// Asked for per request and never stored by the app: an OAuth one expires.
func (s *Server) handleSelfConnectionToken(w http.ResponseWriter, r *http.Request, a *store.App) {
	if s.connections == nil {
		writeError(w, http.StatusNotImplemented, errors.New("connections are not available on this server"))
		return
	}
	tok, err := s.connections.tokenFor(r.Context(), a, r.PathValue("slug"))
	switch {
	case errors.Is(err, errNotConnected):
		// Deliberately different statuses: the owner fixes one in their profile
		// and the other in this app's settings, and an app told the wrong thing
		// sends them to the wrong page.
		writeError(w, http.StatusNotFound, err)
		return
	case errors.Is(err, errNotGranted):
		writeError(w, http.StatusForbidden, err)
		return
	case errors.Is(err, errNotMCPCredential):
		writeError(w, http.StatusBadRequest, err)
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}
