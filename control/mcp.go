package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/mcp"
	"heckel.io/hostit/outbound"
	"heckel.io/hostit/store"
)

// MCP connections. The shape hostit takes here is deliberately different from
// every other provider: hostit SPEAKS MCP rather than handing the credential to
// the app.
//
// The reason is that an MCP token is not scoped to what the app was granted. It
// opens the whole server -- every tool, for the owner's whole account. Handing
// it over would make the grant decorative: an app given "read issues" could send
// any request it liked. So the token stays in control, the app sends a tool name
// and arguments, and hostit makes the call. That also means an app needs no MCP
// client, no OAuth, and no refresh logic, which is most of the work.

const (
	// mcpClientMetadataPath is where hostit publishes the document an
	// authorization server fetches to learn who is asking (Client ID Metadata
	// Documents). It is the client_id, so it must be public and stable.
	mcpClientMetadataPath = "/.well-known/oauth-client"
	// mcpPendingTTL is how long a started consent stays resumable. Long enough
	// to read a consent screen, short enough that abandoned ones do not pile up.
	mcpPendingTTL = 30 * time.Minute
	// mcpToolsCacheTTL is how long a discovered tool list is trusted. Servers do
	// change what they offer, but not every request.
	mcpToolsCacheTTL = 15 * time.Minute
)

var (
	// errNotMCP means the connection is not an MCP server, or is one when the
	// caller wanted a credential.
	errNotMCP = errors.New("that connection is not an MCP server")
)

// mcpMeta is what hostit remembers about an MCP server between requests: where
// it is, what discovery found, and what it offers. Stored as JSON in the
// connection's meta column, which is non-secret by definition -- the token is
// in the sealed secret and never here.
type mcpMeta struct {
	URL       string     `json:"url"`
	Discovery mcpDisco   `json:"discovery,omitempty"`
	Tools     []mcp.Tool `json:"tools,omitempty"`
	ToolsAt   time.Time  `json:"tools_at,omitempty"`
}

// mcpDisco is mcp.Discovery, persisted. It is a separate type because what is
// worth writing down is only the part that does not change per request.
type mcpDisco struct {
	NeedsAuth             bool     `json:"needs_auth,omitempty"`
	Issuer                string   `json:"issuer,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint,omitempty"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	Resource              string   `json:"resource,omitempty"`
	Scopes                []string `json:"scopes,omitempty"`
	SupportsCIMD          bool     `json:"supports_cimd,omitempty"`
	// ClientID is what hostit calls itself to THIS server: the metadata
	// document URL for a CIMD server, or the id a dynamic registration issued.
	// Written down because a registered id must be reused -- registering again
	// on every refresh would leave a trail of dead clients and, on a server that
	// rate-limits it, stop working.
	ClientID string `json:"client_id,omitempty"`
}

func (d mcpDisco) discovery() mcp.Discovery {
	return mcp.Discovery{
		NeedsAuth: d.NeedsAuth, CanAuthorize: d.TokenEndpoint != "",
		Issuer: d.Issuer, AuthorizationEndpoint: d.AuthorizationEndpoint,
		TokenEndpoint: d.TokenEndpoint, RegistrationEndpoint: d.RegistrationEndpoint,
		Resource: d.Resource, Scopes: d.Scopes, SupportsCIMD: d.SupportsCIMD,
	}
}

func fromDiscovery(d mcp.Discovery) mcpDisco {
	return mcpDisco{
		NeedsAuth: d.NeedsAuth, Issuer: d.Issuer,
		AuthorizationEndpoint: d.AuthorizationEndpoint, TokenEndpoint: d.TokenEndpoint,
		RegistrationEndpoint: d.RegistrationEndpoint, Resource: d.Resource,
		Scopes: d.Scopes, SupportsCIMD: d.SupportsCIMD,
	}
}

// mcpPending is a consent in flight. It is held in memory rather than in a
// cookie because the PKCE verifier is the thing that proves the code came back
// to whoever asked for it -- putting it in the browser hands it to whatever can
// read cookies, which is the attack PKCE exists to stop.
type mcpPending struct {
	userID    string
	slug      string
	label     string
	serverURL string
	discovery mcp.Discovery
	// clientID is what hostit called itself when it started this consent. The
	// SAME value must be used to redeem the code, so it is carried rather than
	// re-derived.
	clientID string
	pkce     mcp.PKCE
	expires  time.Time
}

// mcpBroker holds consents in flight. Its lifetime is the process: a control
// restart mid-consent means the owner clicks connect again, which is a far
// better failure than a verifier that outlives the process that made it.
type mcpBroker struct {
	pending map[string]mcpPending
	mu      sync.Mutex // Protects pending
}

func newMCPBroker() *mcpBroker {
	return &mcpBroker{pending: map[string]mcpPending{}}
}

func (b *mcpBroker) put(nonce string, p mcpPending) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range b.pending {
		if time.Now().After(v.expires) {
			delete(b.pending, k)
		}
	}
	b.pending[nonce] = p
}

// take returns a pending consent and removes it, so a code can be redeemed once.
func (b *mcpBroker) take(nonce string) (mcpPending, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[nonce]
	delete(b.pending, nonce)
	if !ok || time.Now().After(p.expires) {
		return mcpPending{}, false
	}
	return p, true
}

// addMCP attaches an MCP server. It returns either a stored connection (the
// server wanted no authorization) or a URL to send the owner to; never both.
func (m *connectionManager) addMCP(ctx context.Context, userID, slug, label, serverURL string) (*store.Connection, string, error) {
	serverURL = strings.TrimSpace(serverURL)
	if err := outbound.CheckURL(serverURL); err != nil {
		return nil, "", fmt.Errorf("%w: %v", connections.ErrInvalidCredential, err)
	}
	disco, err := mcp.Discover(ctx, m.client, serverURL)
	if err != nil {
		return nil, "", fmt.Errorf("cannot reach that MCP server: %w", err)
	}
	if disco.NeedsAuth && !disco.CanAuthorize {
		// Worth naming rather than half-attempting: the owner can do nothing
		// about it here, and a generic failure would send them looking for a
		// mistake they did not make.
		return nil, "", fmt.Errorf("%w: that server requires authorization but does not say how (no OAuth metadata)", connections.ErrInvalidCredential)
	}
	if disco.NeedsAuth {
		return nil, "", errMCPNeedsConsent{discovery: disco}
	}
	conn := &store.Connection{
		ID: store.NewConnectionID(), UserID: userID, Slug: slug, Label: label,
		Provider: connections.ProviderMCP, Kind: store.ConnectionMCP,
		Scopes: strings.Join(disco.Scopes, " "), CreatedAt: time.Now(),
	}
	// An empty secret still gets sealed, so every row in the table is the same
	// shape and nothing downstream has to special-case "this one is plaintext".
	if conn.Secret, err = connections.Seal(m.key, "", connections.Binding(conn.UserID, conn.ID)); err != nil {
		return nil, "", err
	}
	meta := mcpMeta{URL: serverURL, Discovery: fromDiscovery(disco)}
	if tools, err := mcp.NewClient(serverURL, "").ListTools(ctx); err == nil {
		meta.Tools, meta.ToolsAt = tools, time.Now()
	} else {
		slog.Warn("Connected an MCP server that would not list its tools", "url", serverURL, "error", err)
	}
	if conn.Meta, err = encodeMCPMeta(meta); err != nil {
		return nil, "", err
	}
	return conn, "", m.store.AddConnection(conn)
}

// errMCPNeedsConsent carries the discovery result out of addMCP so the handler
// can start a consent without discovering twice.
type errMCPNeedsConsent struct {
	discovery mcp.Discovery
}

func (e errMCPNeedsConsent) Error() string { return "this MCP server requires authorization" }

// saveMCPToken completes a consent, creating the connection or replacing the
// credential on one that already exists (a reconnect keeps the slug and every
// grant naming it).
func (m *connectionManager) saveMCPToken(ctx context.Context, p mcpPending, tok mcp.OAuthToken) error {
	meta := mcpMeta{URL: p.serverURL, Discovery: fromDiscovery(p.discovery)}
	meta.Discovery.ClientID = p.clientID
	if tools, err := mcp.NewClient(p.serverURL, tok.AccessToken).ListTools(ctx); err == nil {
		meta.Tools, meta.ToolsAt = tools, time.Now()
	} else {
		slog.Warn("Authorized an MCP server that would not list its tools", "url", p.serverURL, "error", err)
	}
	encoded, err := encodeMCPMeta(meta)
	if err != nil {
		return err
	}
	scopes := strings.Join(p.discovery.Scopes, " ")
	if existing, err := m.store.ConnectionBySlug(p.userID, p.slug); err == nil {
		sealed, err := connections.Seal(m.key, tok.RefreshToken, connections.Binding(existing.UserID, existing.ID))
		if err != nil {
			return err
		}
		m.expireCachedFor(existing.ID)
		return m.store.UpdateConnectionSecret(existing.ID, sealed, scopes, encoded)
	}
	conn := &store.Connection{
		ID: store.NewConnectionID(), UserID: p.userID, Slug: p.slug, Label: p.label,
		Provider: connections.ProviderMCP, Kind: store.ConnectionMCP,
		Scopes: scopes, Meta: encoded, CreatedAt: time.Now(),
	}
	if conn.Secret, err = connections.Seal(m.key, tok.RefreshToken, connections.Binding(conn.UserID, conn.ID)); err != nil {
		return err
	}
	return m.store.AddConnection(conn)
}

// mcpClientFor returns a client ready to talk to one connection's server,
// refreshing the access token when there is one to refresh.
func (m *connectionManager) mcpClientFor(ctx context.Context, conn *store.Connection, fallbackClientID string) (*mcp.Client, mcpMeta, error) {
	meta, err := decodeMCPMeta(conn.Meta)
	if err != nil {
		return nil, meta, err
	}
	if !meta.Discovery.NeedsAuth {
		return mcp.NewClient(meta.URL, ""), meta, nil
	}
	// Cached against the stored credential, exactly as OAuth connections are:
	// one tool call per page must not be one token round trip per page.
	if tok, ok := m.cachedTokenFor(conn); ok {
		return mcp.NewClient(meta.URL, tok.AccessToken), meta, nil
	}
	refreshToken, err := m.open(conn)
	if err != nil {
		return nil, meta, err
	}
	if refreshToken == "" {
		return nil, meta, fmt.Errorf("%w: reconnect %q", mcp.ErrUnauthorized, conn.Slug)
	}
	// The id this connection was AUTHORIZED with, not whatever the current
	// request would produce: a dynamically registered id has no other source,
	// and a mismatch is refused by the token endpoint.
	clientID := meta.Discovery.ClientID
	if clientID == "" {
		clientID = fallbackClientID
	}
	tok, err := mcp.Refresh(ctx, m.client, meta.Discovery.discovery(), clientID, refreshToken)
	if err != nil {
		return nil, meta, err
	}
	// MCP requires public clients to rotate their refresh token, so the one just
	// spent is dead. Not storing the replacement would make the NEXT call fail
	// with invalid_grant, which reads like the owner revoked access.
	if tok.RefreshToken != "" && tok.RefreshToken != refreshToken {
		sealed, err := connections.Seal(m.key, tok.RefreshToken, connections.Binding(conn.UserID, conn.ID))
		if err != nil {
			return nil, meta, err
		}
		if err := m.store.UpdateConnectionSecret(conn.ID, sealed, conn.Scopes, conn.Meta); err != nil {
			slog.Error("Cannot store a rotated MCP refresh token; this connection will fail on its next use",
				"slug", conn.Slug, "error", err)
		} else {
			conn.Secret = sealed
		}
	}
	m.cache(conn, connections.Token{Provider: connections.ProviderMCP, AccessToken: tok.AccessToken, ExpiresAt: tok.ExpiresAt})
	return mcp.NewClient(meta.URL, tok.AccessToken), meta, nil
}

// mcpTools is what a server offers, from the stored list while it is fresh and
// from the server itself when it is not.
func (m *connectionManager) mcpTools(ctx context.Context, conn *store.Connection, clientID string) ([]mcp.Tool, error) {
	client, meta, err := m.mcpClientFor(ctx, conn, clientID)
	if err != nil {
		return nil, err
	}
	if len(meta.Tools) > 0 && time.Since(meta.ToolsAt) < mcpToolsCacheTTL {
		return meta.Tools, nil
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		if len(meta.Tools) > 0 {
			// A stale list beats no list: the server being briefly unreachable
			// should not make an app's tools vanish.
			slog.Warn("Using a stale MCP tool list", "slug", conn.Slug, "error", err)
			return meta.Tools, nil
		}
		return nil, err
	}
	meta.Tools, meta.ToolsAt = tools, time.Now()
	if encoded, err := encodeMCPMeta(meta); err == nil {
		if err := m.store.UpdateConnectionSecret(conn.ID, conn.Secret, conn.Scopes, encoded); err == nil {
			conn.Meta = encoded
		}
	}
	return tools, nil
}

// mcpCall runs one tool on one connection's server.
func (m *connectionManager) mcpCall(ctx context.Context, conn *store.Connection, clientID, tool string, args map[string]any) (mcp.ToolResult, error) {
	client, _, err := m.mcpClientFor(ctx, conn, clientID)
	if err != nil {
		return mcp.ToolResult{}, err
	}
	return client.CallTool(ctx, tool, args)
}

// grantedMCPConnection resolves an MCP connection an app was granted, refusing
// with the same two distinct errors the credential endpoint uses.
func (m *connectionManager) grantedMCPConnection(a *store.App, slug string) (*store.Connection, error) {
	conn, err := m.store.ConnectionBySlug(a.OwnerID, slug)
	if errors.Is(err, store.ErrConnectionNotFound) {
		return nil, errNotConnected
	} else if err != nil {
		return nil, err
	}
	if conn.Kind != store.ConnectionMCP {
		return nil, errNotMCP
	}
	granted, err := m.store.AppConnections(a.ID)
	if err != nil {
		return nil, err
	}
	if !grantedTo(granted, conn.ID) {
		return nil, errNotGranted
	}
	return conn, nil
}

func encodeMCPMeta(meta mcpMeta) (string, error) {
	b, err := json.Marshal(meta)
	return string(b), err
}

func decodeMCPMeta(raw string) (mcpMeta, error) {
	var meta mcpMeta
	if raw == "" {
		return meta, errNotMCP
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return meta, fmt.Errorf("this connection's server details are unreadable: %w", err)
	}
	return meta, nil
}

// mcpClientID is the URL an authorization server fetches to learn who hostit is.
// Built from the request host so an instance on any hostname identifies itself
// correctly without configuration.
func (s *Server) mcpClientID(r *http.Request) string {
	return s.config.WebURL(hostOnly(r.Host)) + mcpClientMetadataPath
}
