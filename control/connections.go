package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// connectionManager owns credential custody: sealing what is stored, opening
// what is used, and refreshing OAuth tokens. It is the only thing that ever
// holds a refresh token in the clear.
type connectionManager struct {
	store  *store.Store
	key    []byte
	client *http.Client
	conf   *controlconf.Config
}

func newConnectionManager(st *store.Store, key []byte, conf *controlconf.Config) *connectionManager {
	return &connectionManager{
		store:  st,
		key:    key,
		client: &http.Client{Timeout: 20 * time.Second},
		conf:   conf,
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
	sealed, err := connections.Seal(m.key, credential)
	if err != nil {
		return nil, err
	}
	c := &store.Connection{
		UserID: userID, Slug: slug, Label: label, Provider: p.Name, Kind: store.ConnectionOAuth,
		Secret: sealed, Scopes: strings.Join(p.Scopes, " "), CreatedAt: time.Now(),
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
	sealed, err := connections.Seal(m.key, credential)
	if err != nil {
		return err
	}
	return m.store.UpdateConnectionSecret(c.ID, sealed, strings.Join(p.Scopes, " "), c.Meta)
}

// saveStatic stores a pasted credential. Only the field the provider names as
// its secret is sealed; the rest (an IMAP host, an endpoint) is context both
// the UI and the app need to see.
func (m *connectionManager) saveStatic(userID, slug, label string, p connections.Provider, values map[string]string) (*store.Connection, error) {
	if err := p.Validate(values); err != nil {
		return nil, err
	}
	sealed, err := connections.Seal(m.key, values[p.SecretField])
	if err != nil {
		return nil, err
	}
	c := &store.Connection{
		UserID: userID, Slug: slug, Label: label, Provider: p.Name, Kind: store.ConnectionStatic,
		Secret: sealed, Meta: metaFrom(p, values), CreatedAt: time.Now(),
	}
	return c, m.store.AddConnection(c)
}

// updateStatic replaces a pasted credential in place, keeping the slug and
// every grant that names it.
func (m *connectionManager) updateStatic(c *store.Connection, p connections.Provider, values map[string]string) error {
	if err := p.Validate(values); err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, values[p.SecretField])
	if err != nil {
		return err
	}
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
	secret, err := connections.Open(m.key, conn.Secret)
	if err != nil {
		return connections.Token{}, err
	}
	if conn.Kind == store.ConnectionStatic {
		return connections.Token{Provider: p.Name, AccessToken: secret, Meta: conn.Meta}, nil
	}
	id, clientSecret := m.clientFor(p.Name)
	return p.Refresh(ctx, m.client, id, clientSecret, secret)
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
