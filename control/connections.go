package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/store"
)

// Connections: accounts an owner connected once, granted per app, delivered to
// an app as a short-lived token over its own socket. See
// plans/260819-connections.md for why hostit brokers the credential rather than
// proxying each vendor's API.

var (
	// errNotConnected means the owner has not connected this provider.
	errNotConnected = errors.New("not connected")
	// errNotGranted means the connection exists but this app was not granted it.
	// Distinct from errNotConnected on purpose: one is fixed in the profile, the
	// other in the app's settings, and an app told the wrong one sends its owner
	// to the wrong page.
	errNotGranted = errors.New("this app has not been granted that connection")
)

// connectionManager owns credential custody: sealing what is stored, opening
// what is used, and refreshing OAuth tokens. It is the only thing that ever
// holds a refresh token in the clear.
type connectionManager struct {
	store  *store.Store
	key    []byte
	client *http.Client
	conf   connectionConfig
}

// connectionConfig is what the manager needs from the instance's config. The
// redirect URL is NOT here: Google matches it exactly against the host the
// owner actually visited, so it is per request (Config.RedirectURL).
type connectionConfig struct {
	ClientID     string
	ClientSecret string
}

func newConnectionManager(st *store.Store, key []byte, conf connectionConfig) *connectionManager {
	return &connectionManager{
		store:  st,
		key:    key,
		client: &http.Client{Timeout: 20 * time.Second},
		conf:   conf,
	}
}

// available reports whether a provider can be offered here.
func (m *connectionManager) available(p connections.Provider) bool {
	return p.Configured(m.conf.ClientID, m.conf.ClientSecret)
}

// saveOAuth completes a consent flow: the code becomes a refresh token, which
// is sealed before it reaches the database.
func (m *connectionManager) saveOAuth(ctx context.Context, userID string, p connections.Provider, code, redirectURL string) error {
	refresh, err := p.Exchange(ctx, m.client, m.conf.ClientID, m.conf.ClientSecret, redirectURL, code)
	if err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, refresh)
	if err != nil {
		return err
	}
	return m.store.SaveConnection(&store.Connection{
		UserID: userID, Provider: p.Name, Kind: store.ConnectionOAuth,
		Secret: sealed, Scopes: joinScopes(p.Scopes), CreatedAt: time.Now(),
	})
}

// saveStatic stores a pasted credential. Only the field the provider names as
// its secret is sealed; the rest (an IMAP host, a username) is context the UI
// and the app both need to see.
func (m *connectionManager) saveStatic(userID string, p connections.Provider, values map[string]string) error {
	if err := p.Validate(values); err != nil {
		return err
	}
	sealed, err := connections.Seal(m.key, values[p.SecretField])
	if err != nil {
		return err
	}
	meta := ""
	for _, f := range p.Fields {
		if f.Name == p.SecretField {
			continue
		}
		if meta != "" {
			meta += " "
		}
		meta += f.Name + "=" + values[f.Name]
	}
	return m.store.SaveConnection(&store.Connection{
		UserID: userID, Provider: p.Name, Kind: store.ConnectionStatic,
		Secret: sealed, Meta: meta, CreatedAt: time.Now(),
	})
}

// tokenFor is what the app socket answers: a usable credential for one app and
// one provider, or a refusal that says which of the two things is missing.
func (m *connectionManager) tokenFor(ctx context.Context, a *store.App, providerName string) (connections.Token, error) {
	p, ok := connections.Lookup(providerName)
	if !ok {
		return connections.Token{}, fmt.Errorf("unknown connection %q", providerName)
	}
	granted, err := m.store.AppConnections(a.ID)
	if err != nil {
		return connections.Token{}, err
	}
	if !contains(granted, providerName) {
		return connections.Token{}, errNotGranted
	}
	// The OWNER's connection: an app acts as whoever owns it.
	conn, err := m.store.Connection(a.OwnerID, providerName)
	if errors.Is(err, store.ErrConnectionNotFound) {
		return connections.Token{}, errNotConnected
	} else if err != nil {
		return connections.Token{}, err
	}
	secret, err := connections.Open(m.key, conn.Secret)
	if err != nil {
		return connections.Token{}, err
	}
	if conn.Kind == store.ConnectionStatic {
		return connections.Token{Provider: p.Name, AccessToken: secret, Meta: conn.Meta}, nil
	}
	return p.Refresh(ctx, m.client, m.conf.ClientID, m.conf.ClientSecret, secret)
}

func joinScopes(scopes []string) string {
	out := ""
	for _, s := range scopes {
		if out != "" {
			out += " "
		}
		out += s
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
