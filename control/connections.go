package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/controlconf"
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
)

const (
	// tokenCacheMargin is how long before an access token expires it stops
	// being served. Wide enough that a token handed out now is still valid by
	// the time the app has finished using it.
	tokenCacheMargin = 2 * time.Minute
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
		store:  st,
		key:    key,
		client: &http.Client{Timeout: 20 * time.Second},
		conf:   conf,
		cached: map[string]cachedToken{},
	}
}

// clientFor is the OAuth client this instance holds for a provider. Read per
// call rather than captured, so configuring one takes effect without a restart.
func (m *connectionManager) clientFor(provider string) (id, secret string) {
	return m.conf.ConnectionClient(provider)
}

// available reports whether a provider can be offered here at all.
func (m *connectionManager) available(p connections.Provider) bool {
	return p.Configured(m.clientFor(p.Name))
}

// offered is every provider this instance can actually connect, for the UI. A
// provider whose client is not configured is not shown rather than shown and
// broken.
func (m *connectionManager) offered() []connections.Provider {
	out := make([]connections.Provider, 0)
	for _, p := range connections.All() {
		if m.available(p) {
			out = append(out, p)
		}
	}
	return out
}

// saveOAuth completes a consent flow. What gets stored is a refresh token, or
// -- for a provider whose token does not expire -- the access token itself;
// either way it is sealed before it reaches the database.
func (m *connectionManager) saveOAuth(ctx context.Context, userID, slug, label string, p connections.Provider, code, redirectURL string) (*store.Connection, error) {
	id, secret := m.clientFor(p.Name)
	credential, err := p.Exchange(ctx, m.client, id, secret, redirectURL, code)
	if err != nil {
		return nil, err
	}
	// The id is assigned here rather than by the store, because the credential
	// is sealed BOUND to it (see connections.Binding) and cannot be sealed
	// before it exists.
	c := &store.Connection{
		ID: store.NewConnectionID(), UserID: userID, Slug: slug, Label: label,
		Provider: p.Name, Kind: store.ConnectionOAuth,
		Scopes: strings.Join(p.Scopes, " "), CreatedAt: time.Now(),
	}
	if c.Secret, err = connections.Seal(m.key, credential, connections.Binding(c.UserID, c.ID)); err != nil {
		return nil, err
	}
	return c, m.store.AddConnection(c)
}

// reconnect replaces the credential on an existing connection, which is what a
// re-consent is: same slug, same grants, fresh secret. Apps keep working.
func (m *connectionManager) reconnect(ctx context.Context, c *store.Connection, p connections.Provider, code, redirectURL string) error {
	id, secret := m.clientFor(p.Name)
	credential, err := p.Exchange(ctx, m.client, id, secret, redirectURL, code)
	if err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, credential, connections.Binding(c.UserID, c.ID))
	if err != nil {
		return err
	}
	m.expireCachedFor(c.ID)
	return m.store.UpdateConnectionSecret(c.ID, sealed, strings.Join(p.Scopes, " "), c.Meta)
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
	p, ok := connections.Lookup(conn.Provider)
	if !ok {
		return connections.Token{}, fmt.Errorf("unknown provider %q", conn.Provider)
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
	id, clientSecret := m.clientFor(p.Name)
	tok, rotated, err := p.Refresh(ctx, m.client, id, clientSecret, secret)
	if err != nil {
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
			// The token in hand is still good, so answer with it -- but say so,
			// because the connection is now one request from breaking.
			slog.Error("Cannot store a rotated refresh token; this connection will fail on its next use",
				"slug", conn.Slug, "provider", conn.Provider, "error", err)
		}
		// Cache against the NEW credential, or the next request would see a
		// mismatch and refresh again immediately.
		conn.Secret = sealed
	}
	m.cache(conn, tok)
	return tok, nil
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
