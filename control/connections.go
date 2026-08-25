package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/mcp"
	"heckel.io/hostit/outbound"
	"heckel.io/hostit/store"
)

// Connections and credentials: the accounts and secrets an owner attaches once,
// grants per app, and which an app reads as a short-lived token over its own
// socket. hostit brokers the CREDENTIAL, not the API -- see
// plans/260819-connections.md for why that is the right trade here.

var (
	// errNotConnected means the owner has nothing by that name.
	errNotConnected = errors.New("no connection by that name")
	// errNotGranted means it exists but this app was not given it. Distinct from
	// errNotConnected on purpose: one is fixed in the profile, the other in the
	// app's settings, and an app told the wrong one sends its owner to the wrong
	// page.
	errNotGranted = errors.New("this app has not been granted that connection")
	// errNotMCPCredential means the app asked for a credential it is not given.
	errNotMCPCredential = errors.New("that connection has no credential to hand out")
)

const (
	// tokenCacheMargin is how long before an access token expires it stops
	// being served. Wide enough that a token handed out now is still valid by
	// the time the app has finished using it.
	tokenCacheMargin = 2 * time.Minute
	// connectionRefreshInterval is how often the proactive sweep runs;
	// connectionRefreshHorizon is how far ahead of expiry it refreshes, so a
	// token is renewed comfortably before it lapses.
	connectionRefreshInterval = 10 * time.Minute
	connectionRefreshHorizon  = 15 * time.Minute
	// discoveryTimeout bounds asking a custom provider's issuer where its
	// endpoints are. Short: it is on the path of rendering the Add menu.
	discoveryTimeout = 10 * time.Second
)

// connectionManager owns credential custody: sealing what is stored, opening
// what is used, and refreshing OAuth tokens. It is the only thing that ever
// holds a refresh token in the clear.
type connectionManager struct {
	store  *store.Store
	key    []byte
	client *http.Client
	conf   *controlconf.Config
	// cached holds the last access token minted for a connection, so a page
	// that makes five requests is not five round trips to the provider for a
	// token that is good for an hour -- and, on a rotating provider, five
	// database writes as well.
	cached  map[string]cachedToken
	cacheMu sync.Mutex // Protects cached
	// refreshStop ends the proactive refresh loop on shutdown.
	refreshStop chan struct{}
	// custom are the providers this operator wrote in control.yml; discovered
	// holds the endpoints resolved for the ones that gave an issuer instead of
	// writing them out.
	custom     map[string]connections.Provider
	discovered map[string]oauthEndpoints
	customMu   sync.Mutex // Protects custom and discovered
}

// oauthEndpoints is one provider's resolved authorize and token URLs.
type oauthEndpoints struct {
	authURL  string
	tokenURL string
}

// cachedToken is one connection's live access token.
//
// mintedFrom is the SEALED credential it came from. Comparing that against
// what the database currently holds is how the entry invalidates itself: a
// reconnect replaces the credential, so the entry no longer matches and is
// discarded without anyone having to remember to purge it.
type cachedToken struct {
	token      connections.Token
	mintedFrom string
	expires    time.Time
}

func newConnectionManager(st *store.Store, key []byte, conf *controlconf.Config) *connectionManager {
	return &connectionManager{
		store: st,
		key:   key,
		// GUARDED: this client fetches URLs users supply -- an MCP server's
		// endpoint, a custom provider's issuer -- so it must refuse to connect
		// anywhere that is not publicly routable. See the outbound package.
		client:      outbound.NewClient(20*time.Second, conf.OutboundAllowPrivate),
		conf:        conf,
		cached:      map[string]cachedToken{},
		refreshStop: make(chan struct{}),
		custom:      map[string]connections.Provider{},
		discovered:  map[string]oauthEndpoints{},
	}
}

// clientFor is the OAuth client this instance holds for a provider. Read per
// call rather than captured, so configuring one takes effect without a restart.
func (m *connectionManager) clientFor(provider string) (id, secret string) {
	return m.conf.ConnectionClient(provider)
}

// available reports whether a provider can be offered here at all.
func (m *connectionManager) available(p connections.Provider) bool {
	// An unresolved custom provider has no endpoints to send anyone to, so it
	// is not offered until discovery succeeds.
	if p.NeedsDiscovery() {
		return false
	}
	return p.Configured(m.clientFor(p.Name))
}

// offered is every provider this instance can actually connect, for the UI. A
// provider whose client is not configured -- or whose endpoints could not be
// discovered -- is not shown rather than shown and broken.
func (m *connectionManager) offered() []connections.Provider {
	out := make([]connections.Provider, 0)
	for _, p := range append(connections.All(), m.customProviders()...) {
		if m.available(p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// saveOAuth completes a consent flow. What gets stored is a refresh token, or
// -- for a provider whose token does not expire -- the access token itself;
// either way it is sealed before it reaches the database.
func (m *connectionManager) saveOAuth(ctx context.Context, userID, slug, label string, p connections.Provider, code, redirectURL, scopes string) (*store.Connection, error) {
	id, secret := m.clientForUser(userID, p.Name)
	credential, err := p.Exchange(ctx, m.client, id, secret, redirectURL, code)
	if err != nil {
		return nil, err
	}
	// The id is assigned here rather than by the store, because the credential
	// is sealed BOUND to it (see connections.Binding) and cannot be sealed
	// before it exists.
	if scopes == "" {
		scopes = strings.Join(p.Scopes, " ")
	}
	c := &store.Connection{
		ID: store.NewConnectionID(), UserID: userID, Slug: slug, Label: label,
		Provider: p.Name, Kind: store.ConnectionOAuth,
		Scopes: scopes, CreatedAt: time.Now(),
	}
	if c.Secret, err = connections.Seal(m.key, credential, connections.Binding(c.UserID, c.ID)); err != nil {
		return nil, err
	}
	return c, m.store.AddConnection(c)
}

// reconnect replaces the credential on an existing connection, which is what a
// re-consent is: same slug, same grants, fresh secret. Apps keep working.
func (m *connectionManager) reconnect(ctx context.Context, c *store.Connection, p connections.Provider, code, redirectURL string) error {
	id, secret := m.clientForUser(c.UserID, p.Name)
	credential, err := p.Exchange(ctx, m.client, id, secret, redirectURL, code)
	if err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, credential, connections.Binding(c.UserID, c.ID))
	if err != nil {
		return err
	}
	m.expireCachedFor(c.ID)
	_ = m.store.SetConnectionStatus(c.ID, store.ConnectionStatusOK) // a fresh consent clears needs-reconnect
	// A re-consent keeps the same grants; store what the connection already had,
	// not the provider baseline (which would wipe the options the owner chose).
	return m.store.UpdateConnectionSecret(c.ID, sealed, c.Scopes, c.Meta)
}

// saveStatic stores a pasted credential. Only the field the provider names as
// its secret is sealed; the rest (an IMAP host, an endpoint) is context both
// the UI and the app need to see.
func (m *connectionManager) saveStatic(userID, slug, label string, p connections.Provider, values map[string]string) (*store.Connection, error) {
	if err := p.Validate(values); err != nil {
		return nil, err
	}
	c := &store.Connection{
		ID: store.NewConnectionID(), UserID: userID, Slug: slug, Label: label,
		Provider: p.Name, Kind: store.ConnectionStatic,
		Meta: metaFrom(p, values), CreatedAt: time.Now(),
	}
	sealed, err := connections.Seal(m.key, values[p.SecretField], connections.Binding(c.UserID, c.ID))
	if err != nil {
		return nil, err
	}
	c.Secret = sealed
	return c, m.store.AddConnection(c)
}

// updateStatic replaces a pasted credential in place, keeping the slug and
// every grant that names it.
func (m *connectionManager) updateStatic(c *store.Connection, p connections.Provider, values map[string]string) error {
	if err := p.Validate(values); err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, values[p.SecretField], connections.Binding(c.UserID, c.ID))
	if err != nil {
		return err
	}
	m.expireCachedFor(c.ID)
	return m.store.UpdateConnectionSecret(c.ID, sealed, c.Scopes, metaFrom(p, values))
}

// tokenFor is what the app socket answers: a usable credential for one app and
// one slug, or a refusal that says which of the two things is missing.
func (m *connectionManager) tokenFor(ctx context.Context, a *store.App, slug string) (connections.Token, error) {
	// Resolved in the APP OWNER's namespace: an app acts as whoever owns it, and
	// a slug is only ever meaningful within one person's connections.
	conn, err := m.store.ConnectionBySlug(a.OwnerID, slug)
	if errors.Is(err, store.ErrConnectionNotFound) {
		return connections.Token{}, errNotConnected
	} else if err != nil {
		return connections.Token{}, err
	}
	granted, err := m.store.AppConnections(a.ID)
	if err != nil {
		return connections.Token{}, err
	}
	if !grantedTo(granted, conn.ID) {
		return connections.Token{}, errNotGranted
	}
	// Resolved for the connection's OWNER: a personal provider exists only in
	// their namespace, and an app acts as whoever owns it.
	p, ok := m.providerFor(conn.UserID, conn.Provider)
	if !ok {
		return connections.Token{}, fmt.Errorf("unknown provider %q", conn.Provider)
	}
	// An MCP credential is not scoped to what the app was granted -- it opens
	// the whole server. Handing it over would make the grant decorative, so the
	// token stays here and the app calls tools through hostit instead.
	if conn.Kind == store.ConnectionMCP {
		return connections.Token{}, fmt.Errorf("%w: %q is an mcp server; call its tools at /v1/mcp/%s/call", errNotMCPCredential, conn.Slug, conn.Slug)
	}
	secret, err := m.open(conn)
	if err != nil {
		return connections.Token{}, err
	}
	if conn.Kind == store.ConnectionStatic {
		return connections.Token{Provider: p.Name, AccessToken: secret, Meta: conn.Meta}, nil
	}
	// Served from cache only when it came from the credential the database
	// still holds and has real life left. The grant was already checked above,
	// so revoking one takes effect immediately no matter how warm this is.
	if tok, ok := m.cachedTokenFor(conn); ok {
		return tok, nil
	}
	return m.refreshAndStore(ctx, conn, p, secret)
}

// refreshAndStore exchanges the stored refresh token for an access token, stores
// any rotated refresh token, updates the connection's health, and caches the
// result. Shared by the on-demand token path, the proactive sweep, and verify.
func (m *connectionManager) refreshAndStore(ctx context.Context, conn *store.Connection, p connections.Provider, secret string) (connections.Token, error) {
	id, clientSecret := m.clientForUser(conn.UserID, p.Name)
	tok, rotated, err := p.Refresh(ctx, m.client, id, clientSecret, secret)
	if err != nil {
		// A rejected credential means the owner must reconnect; a transient
		// error leaves the health untouched so a blip is not reported as broken.
		if errors.Is(err, connections.ErrReauthRequired) {
			m.setStatus(conn, store.ConnectionStatusNeedsReconnect)
		}
		return connections.Token{}, err
	}
	// Some providers rotate the refresh token on every use (Discord does), and
	// the one we just spent is dead. Storing the replacement is not optional:
	// without it the NEXT request fails with invalid_grant, which reads like
	// the owner revoked access when nothing of the sort happened.
	if rotated != "" {
		sealed, err := connections.Seal(m.key, rotated, connections.Binding(conn.UserID, conn.ID))
		if err != nil {
			return connections.Token{}, err
		}
		if err := m.store.UpdateConnectionSecret(conn.ID, sealed, conn.Scopes, conn.Meta); err != nil {
			slog.Error("Cannot store a rotated refresh token; this connection will fail on its next use",
				"slug", conn.Slug, "provider", conn.Provider, "error", err)
		}
		// Cache against the NEW credential, or the next request would see a
		// mismatch and refresh again immediately.
		conn.Secret = sealed
	}
	// A successful refresh clears any prior needs-reconnect.
	m.setStatus(conn, store.ConnectionStatusOK)
	m.cache(conn, tok)
	return tok, nil
}

// setStatus persists a connection's health, in-place, only when it changed.
func (m *connectionManager) setStatus(conn *store.Connection, status string) {
	if conn.Status == status {
		return
	}
	if err := m.store.SetConnectionStatus(conn.ID, status); err != nil {
		slog.Warn("Cannot update connection health", "slug", conn.Slug, "status", status, "error", err)
		return
	}
	conn.Status = status
}

// cacheFreshFor reports whether the cached access token, minted from the CURRENT
// stored credential, is still valid at least `horizon` from now.
func (m *connectionManager) cacheFreshFor(conn *store.Connection, horizon time.Duration) bool {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	e, ok := m.cached[conn.ID]
	return ok && e.mintedFrom == conn.Secret && time.Now().Add(horizon).Before(e.expires)
}

// refreshDue proactively refreshes every OAuth connection whose access token is
// missing or about to expire, so a connection stays alive without the owner
// re-authorizing. Long-lived-token providers (Slack, GitHub) have nothing to
// refresh and are skipped; their health only moves on a real use or verify.
func (m *connectionManager) refreshDue(ctx context.Context) {
	conns, err := m.store.AllConnections()
	if err != nil {
		slog.Warn("Proactive refresh: cannot list connections", "error", err)
		return
	}
	for _, conn := range conns {
		if conn.Kind != store.ConnectionOAuth {
			continue
		}
		p, ok := m.providerFor(conn.UserID, conn.Provider)
		if !ok || p.LongLivedToken {
			continue
		}
		if m.cacheFreshFor(conn, connectionRefreshHorizon) {
			continue // still warm well past the next sweep
		}
		secret, err := m.open(conn)
		if err != nil {
			continue
		}
		if _, err := m.refreshAndStore(ctx, conn, p, secret); err != nil && !errors.Is(err, connections.ErrReauthRequired) {
			slog.Warn("Proactive refresh failed", "slug", conn.Slug, "provider", conn.Provider, "error", err)
		}
	}
}

// RefreshLoop keeps OAuth tokens warm until stopped: once at start, then every
// interval.
func (m *connectionManager) RefreshLoop(interval time.Duration) {
	m.refreshDue(context.Background())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.refreshDue(context.Background())
		case <-m.refreshStop:
			return
		}
	}
}

// StopRefresh ends the proactive refresh loop.
func (m *connectionManager) StopRefresh() {
	select {
	case <-m.refreshStop:
	default:
		close(m.refreshStop)
	}
}

// Verify actively checks one connection's health by refreshing it now, and
// returns the resulting status. A long-lived or static connection has nothing to
// refresh, so its stored status is returned as-is.
func (m *connectionManager) Verify(ctx context.Context, userID, slug string) (string, error) {
	conn, err := m.store.ConnectionBySlug(userID, slug)
	if err != nil {
		return "", err
	}
	p, ok := m.providerFor(conn.UserID, conn.Provider)
	if !ok {
		return "", fmt.Errorf("unknown provider %q", conn.Provider)
	}
	if conn.Kind != store.ConnectionOAuth || p.LongLivedToken {
		return conn.Status, nil
	}
	secret, err := m.open(conn)
	if err != nil {
		return "", err
	}
	if _, err := m.refreshAndStore(ctx, conn, p, secret); err != nil {
		if errors.Is(err, connections.ErrReauthRequired) {
			return store.ConnectionStatusNeedsReconnect, nil
		}
		return "", err
	}
	return store.ConnectionStatusOK, nil
}

// open decrypts a stored credential, tolerating one sealed before credentials
// were bound to their row and quietly re-sealing it bound. Without the re-seal
// an instance that predates the change would never converge -- a static
// credential is never rewritten on its own.
func (m *connectionManager) open(conn *store.Connection) (string, error) {
	aad := connections.Binding(conn.UserID, conn.ID)
	secret, bound, err := connections.OpenLegacyTolerant(m.key, conn.Secret, aad)
	if err != nil {
		return "", err
	}
	if !bound {
		if sealed, sealErr := connections.Seal(m.key, secret, aad); sealErr == nil {
			if storeErr := m.store.UpdateConnectionSecret(conn.ID, sealed, conn.Scopes, conn.Meta); storeErr == nil {
				conn.Secret = sealed
			} else {
				slog.Warn("Cannot re-seal a credential against its row", "slug", conn.Slug, "error", storeErr)
			}
		}
	}
	return secret, nil
}

// cachedTokenFor returns a live token for this connection, if there is one that
// was minted from the credential the database still holds.
func (m *connectionManager) cachedTokenFor(conn *store.Connection) (connections.Token, bool) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	entry, ok := m.cached[conn.ID]
	if !ok || entry.mintedFrom != conn.Secret || time.Now().After(entry.expires) {
		return connections.Token{}, false
	}
	return entry.token, true
}

// cache remembers a freshly minted token until shortly before it expires. A
// token with no expiry is not cached: it did not cost a round trip to produce.
func (m *connectionManager) cache(conn *store.Connection, tok connections.Token) {
	if tok.ExpiresAt == nil {
		return
	}
	expires := tok.ExpiresAt.Add(-tokenCacheMargin)
	if !expires.After(time.Now()) {
		return // already too close to expiry to be worth handing out again
	}
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	// Drop anything long dead while we are here; the map is bounded by the
	// number of connections, but there is no reason to keep corpses.
	for id, e := range m.cached {
		if time.Now().After(e.expires) {
			delete(m.cached, id)
		}
	}
	m.cached[conn.ID] = cachedToken{token: tok, mintedFrom: conn.Secret, expires: expires}
}

// RotateKey re-seals every stored credential under a fresh key and replaces the
// key file. It exists so that a leaked key has an answer other than asking every
// user to re-authorise every account.
//
// Everything is re-sealed BEFORE the key file is replaced, and only then is the
// manager switched over, so a failure part-way leaves the old key still on disk
// and still correct for whatever has not been rewritten yet. The re-seal keeps
// each credential bound to its own row, so rotation does not quietly make
// ciphertext portable again.
//
// Returns how many credentials were re-sealed.
func (m *connectionManager) RotateKey() (int, error) {
	fresh, err := connections.NewKey()
	if err != nil {
		return 0, err
	}
	all, err := m.store.AllConnections()
	if err != nil {
		return 0, err
	}
	type resealed struct {
		id, secret string
	}
	pending := make([]resealed, 0, len(all))
	for _, c := range all {
		plain, err := m.open(c)
		if err != nil {
			// Refuse the whole rotation: half-rotated is the one state with no
			// single key that opens everything.
			return 0, fmt.Errorf("cannot open %q before rotating; nothing was changed: %w", c.Slug, err)
		}
		sealed, err := connections.Seal(fresh, plain, connections.Binding(c.UserID, c.ID))
		if err != nil {
			return 0, err
		}
		pending = append(pending, resealed{id: c.ID, secret: sealed})
	}
	for _, p := range pending {
		if err := m.store.UpdateConnectionSecretOnly(p.id, p.secret); err != nil {
			return 0, fmt.Errorf("rotation failed part-way at %s; the previous key is kept as connections.key.previous: %w", p.id, err)
		}
	}
	if err := connections.ReplaceKey(m.conf.DataDir, m.key, fresh); err != nil {
		return 0, err
	}
	m.key = fresh
	m.cacheMu.Lock()
	m.cached = map[string]cachedToken{}
	m.cacheMu.Unlock()
	slog.Info("Credential key rotated", "connections", len(pending))
	return len(pending), nil
}

// expireCachedFor drops a connection's cached token. Reconnecting and
// disconnecting both invalidate on their own (the credential changes, or the row
// goes), so this exists for tests and for an explicit purge.
func (m *connectionManager) expireCachedFor(connectionID string) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	delete(m.cached, connectionID)
}

// metaFrom collects a static provider's non-secret fields, which the app reads
// alongside the credential (an IMAP host is useless without its username).
func metaFrom(p connections.Provider, values map[string]string) string {
	parts := make([]string, 0, len(p.Fields))
	for _, f := range p.Fields {
		if f.Name == p.SecretField || values[f.Name] == "" {
			continue
		}
		parts = append(parts, f.Name+"="+values[f.Name])
	}
	return strings.Join(parts, " ")
}

func grantedTo(granted []*store.Connection, id string) bool {
	for _, c := range granted {
		if c.ID == id {
			return true
		}
	}
	return false
}

// ---- Operator-written providers -------------------------------------------

// loadCustomProviders turns the entries in control.yml that describe a whole
// provider (rather than just a client for one hostit ships) into providers this
// instance offers.
//
// They live HERE, on the manager, and not in the connections package's global
// catalog. Registering into a global at startup would leak between servers in
// one test binary and make the set of providers depend on what else had run.
//
// A broken entry is an error, not a warning: the operator is looking at the file
// they just edited, and a provider silently missing from a menu is the hardest
// possible way to find out it is malformed.
func (m *connectionManager) loadCustomProviders(conf *controlconf.Config) error {
	custom := make(map[string]connections.Provider)
	for name, client := range conf.ConnectionClients {
		if !client.DescribesProvider() {
			continue // just a client for a provider hostit already ships
		}
		p, err := connections.CustomProvider(name, connections.CustomSpec{
			Label:          client.Label,
			Scopes:         client.Scopes,
			Issuer:         client.Issuer,
			AuthURL:        client.AuthURL,
			TokenURL:       client.TokenURL,
			AuthParams:     client.AuthParams,
			LongLivedToken: client.LongLivedToken,
			Help:           client.Help,
			NameHint:       client.NameHint,
		})
		if err != nil {
			return fmt.Errorf("connections: %w", err)
		}
		custom[name] = p
	}
	m.customMu.Lock()
	defer m.customMu.Unlock()
	m.custom = custom
	return nil
}

// lookup resolves a provider by name, preferring this instance's own entries.
// Everything that used connections.Lookup for a CONNECTABLE provider goes
// through here, or an operator's provider would be invisible to half the code.
func (m *connectionManager) lookup(name string) (connections.Provider, bool) {
	m.customMu.Lock()
	p, ok := m.custom[name]
	m.customMu.Unlock()
	if ok {
		return m.resolved(p), true
	}
	return connections.Lookup(name)
}

// resolved fills in the endpoints of a provider whose operator gave an issuer
// instead, asking the service's own metadata where they are -- the same walk
// hostit does for an MCP server.
//
// Done on FIRST USE rather than at startup: resolving needs the network, and a
// blip while control happens to be restarting should not silently drop a
// provider from the menu for the rest of the process's life. Cached once it
// succeeds, so it costs one request per provider per restart.
func (m *connectionManager) resolved(p connections.Provider) connections.Provider {
	if !p.NeedsDiscovery() {
		return p
	}
	m.customMu.Lock()
	if endpoints, ok := m.discovered[p.Issuer]; ok { // keyed by issuer: two tenants may share a provider NAME but not an issuer
		m.customMu.Unlock()
		return p.WithEndpoints(endpoints.authURL, endpoints.tokenURL)
	}
	m.customMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	meta, err := mcp.AuthServerMetadata(ctx, m.client, p.Issuer)
	if err != nil || meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		// Returned unresolved: Configured() then reports it as unavailable, so
		// it is not offered rather than offered and broken.
		slog.Warn("Cannot discover a custom provider's OAuth endpoints; it will not be offered",
			"provider", p.Name, "issuer", p.Issuer, "error", err)
		return p
	}
	m.customMu.Lock()
	m.discovered[p.Issuer] = oauthEndpoints{authURL: meta.AuthorizationEndpoint, tokenURL: meta.TokenEndpoint}
	m.customMu.Unlock()
	slog.Info("Discovered a custom provider's OAuth endpoints",
		"provider", p.Name, "authorize", meta.AuthorizationEndpoint, "token", meta.TokenEndpoint)
	return p.WithEndpoints(meta.AuthorizationEndpoint, meta.TokenEndpoint)
}

// customProviders is this instance's own entries, resolved, for the menu.
func (m *connectionManager) customProviders() []connections.Provider {
	m.customMu.Lock()
	names := make([]string, 0, len(m.custom))
	for name := range m.custom {
		names = append(names, name)
	}
	m.customMu.Unlock()
	sort.Strings(names)
	out := make([]connections.Provider, 0, len(names))
	for _, name := range names {
		if p, ok := m.lookup(name); ok {
			out = append(out, p)
		}
	}
	return out
}
