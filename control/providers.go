package control

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"heckel.io/hostit/control/connections"
	"heckel.io/hostit/store"
)

// Provider definitions, in three tiers.
//
//	1. hostit's own catalog     connections/providers.go, in the binary
//	2. the operator's           control.yml, or the provider table with no owner
//	3. a user's own             the provider table, owned by them
//
// A user having their own OAuth client is an ordinary thing, not a workaround:
// you register an app with the vendor, point it at hostit's callback, and paste
// the pair in. Nothing about OAuth requires the client to belong to the
// instance. The only thing that IS instance-level is the callback URL, which is
// why the UI shows it rather than making people work it out.
//
// Resolution goes down the tiers in order, and CREATION refuses a name any
// higher tier already uses. So an operator can always rely on what a name
// means, and a user can never quietly redefine "github" for their own apps.

// tierProvider is a provider plus where it came from, which the UI shows and
// the delete path checks.
type tierProvider struct {
	provider connections.Provider
	// ownerID is empty for a built-in or an operator's; otherwise the user
	// whose definition it is.
	ownerID string
	// id is the row id, empty for a built-in or a control.yml entry -- neither
	// of which can be edited through the API.
	id string
}

// providerFor resolves a name for one user, across every tier.
func (m *connectionManager) providerFor(userID, name string) (connections.Provider, bool) {
	if p, ok := m.lookup(name); ok {
		return p, true // built-in, or control.yml
	}
	row, err := m.store.ProviderByName(userID, name)
	if err != nil {
		return connections.Provider{}, false
	}
	p, err := m.providerFromRow(row)
	if err != nil {
		return connections.Provider{}, false
	}
	return m.resolved(p), true
}

// providerFromRow turns a stored definition into a provider, unsealing the
// client secret. The secret is the only part that was ever encrypted; a client
// ID is public by design and an endpoint is not a secret at all.
func (m *connectionManager) providerFromRow(row *store.Provider) (connections.Provider, error) {
	if row.Kind == store.ProviderMCP {
		// An MCP preset is not a connectable provider in the OAuth sense: it is
		// a URL offered so a user does not have to know it. It reaches the UI
		// through offeredMCPServers, not here.
		return connections.Provider{}, fmt.Errorf("%s is an MCP server, not an OAuth provider", row.Name)
	}
	var params map[string]string
	if row.AuthParams != "" {
		_ = json.Unmarshal([]byte(row.AuthParams), &params)
	}
	p, err := connections.CustomProvider(row.Name, connections.CustomSpec{
		Label:          row.Label,
		Scopes:         splitScopes(row.Scopes),
		Issuer:         row.Issuer,
		AuthURL:        row.AuthURL,
		TokenURL:       row.TokenURL,
		AuthParams:     params,
		LongLivedToken: row.LongLived,
		Help:           row.Help,
		NameHint:       row.NameHint,
	})
	if err != nil {
		return connections.Provider{}, err
	}
	p.Personal = row.OwnerID != ""
	return p, nil
}

// clientForUser is clientFor with the stored tiers behind it: a user's own
// client wins for their own provider, because it is the only one that exists
// for it.
func (m *connectionManager) clientForUser(userID, provider string) (id, secret string) {
	if id, secret := m.clientFor(provider); id != "" {
		return id, secret
	}
	row, err := m.store.ProviderByName(userID, provider)
	if err != nil || row.Kind != store.ProviderOAuth {
		return "", ""
	}
	plain, err := m.openProviderSecret(row)
	if err != nil {
		return "", ""
	}
	return row.ClientID, plain
}

// openProviderSecret unseals a stored client secret, bound to its own row the
// same way a connection's credential is.
func (m *connectionManager) openProviderSecret(row *store.Provider) (string, error) {
	if row.ClientSecret == "" {
		return "", nil
	}
	owner := row.OwnerID
	if owner == "" {
		owner = instanceOwner
	}
	return connections.Open(m.key, row.ClientSecret, connections.Binding(owner, row.ID))
}

// sealProviderSecret is the other half.
func (m *connectionManager) sealProviderSecret(row *store.Provider, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	owner := row.OwnerID
	if owner == "" {
		owner = instanceOwner
	}
	return connections.Seal(m.key, plain, connections.Binding(owner, row.ID))
}

// instanceOwner stands in for "nobody" in a credential binding, so an instance
// provider's secret is still bound to something stable rather than to the empty
// string.
const instanceOwner = "hostit-instance"

// availableFor is `available` for one user: a personal provider's client comes
// from their own row, not from control.yml, so the instance-only check would
// offer it in the menu and then refuse it on click.
func (m *connectionManager) availableFor(userID string, p connections.Provider) bool {
	if p.NeedsDiscovery() {
		return false
	}
	if p.Kind != connections.KindOAuth {
		return true
	}
	id, secret := m.clientForUser(userID, p.Name)
	return id != "" && secret != ""
}

// offeredFor is every provider this USER can connect: the built-ins and the
// operator's, plus their own.
func (m *connectionManager) offeredFor(userID string) []connections.Provider {
	out := m.offered()
	seen := make(map[string]bool, len(out))
	for _, p := range out {
		seen[p.Name] = true
	}
	rows, err := m.store.ProvidersFor(userID)
	if err != nil {
		return out
	}
	for _, row := range rows {
		if row.Kind != store.ProviderOAuth || seen[row.Name] {
			continue
		}
		p, err := m.providerFromRow(row)
		if err != nil {
			continue
		}
		if p = m.resolved(p); m.availableWithClient(p, row) {
			out = append(out, p)
		}
	}
	return out
}

// availableWithClient is `available` for a stored provider, whose client comes
// from the row rather than from control.yml.
func (m *connectionManager) availableWithClient(p connections.Provider, row *store.Provider) bool {
	if p.NeedsDiscovery() {
		return false
	}
	return row.ClientID != ""
}

// offeredMCPServers is the named MCP servers on offer: the operator's from
// control.yml, the operator's from the database, and the user's own. Purely a
// shortcut -- anyone can still paste any URL.
func (m *connectionManager) offeredMCPServers(userID string) []*store.Provider {
	out := make([]*store.Provider, 0)
	seen := map[string]bool{}
	for name, srv := range m.conf.MCPServers {
		label := srv.Label
		if label == "" {
			label = name
		}
		seen[name] = true
		out = append(out, &store.Provider{Name: name, Label: label, Kind: store.ProviderMCP, URL: srv.URL, Help: srv.Help})
	}
	rows, err := m.store.ProvidersFor(userID)
	if err == nil {
		for _, row := range rows {
			if row.Kind == store.ProviderMCP && !seen[row.Name] {
				out = append(out, row)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// nameAvailableFor reports whether a user may define this name, refusing one
// any higher tier already uses. Their own existing definition is not a clash --
// that is an edit.
//
// The catalog check applies to OAUTH definitions only. An MCP preset's name is
// a menu label and nothing else -- it never becomes a connection's provider, so
// it shares no namespace with the OAuth providers. Refusing to let an operator
// call their Linear MCP server "linear" because an OAuth provider of that name
// exists would be an error about nothing.
func (m *connectionManager) nameAvailableFor(userID, name, kind, exceptID string) error {
	if kind == store.ProviderOAuth {
		if _, taken := connections.Lookup(name); taken {
			return fmt.Errorf("%w: %q is one of hostit's own providers", connections.ErrInvalidCredential, name)
		}
		m.customMu.Lock()
		_, operatorHas := m.custom[name]
		m.customMu.Unlock()
		if operatorHas {
			return fmt.Errorf("%w: %q is defined by this server's operator", connections.ErrInvalidCredential, name)
		}
	}
	if row, err := m.store.ProviderByName(userID, name); err == nil && row.ID != exceptID {
		if row.OwnerID == "" {
			return fmt.Errorf("%w: %q is defined by this server's operator", connections.ErrInvalidCredential, name)
		}
		return store.ErrProviderExists
	}
	return nil
}

// saveProvider creates or replaces a definition, sealing the client secret.
// ownerID is empty for an instance provider.
func (m *connectionManager) saveProvider(row *store.Provider, clientSecret string, existing *store.Provider) error {
	if existing != nil {
		row.ID, row.CreatedAt = existing.ID, existing.CreatedAt
		// An unchanged secret is not resent by the UI, so an empty one means
		// "leave it alone" rather than "erase it".
		if clientSecret == "" {
			row.ClientSecret = existing.ClientSecret
		} else {
			sealed, err := m.sealProviderSecret(row, clientSecret)
			if err != nil {
				return err
			}
			row.ClientSecret = sealed
		}
		return m.store.UpdateProvider(row)
	}
	row.ID = store.NewProviderID()
	row.CreatedAt = time.Now()
	sealed, err := m.sealProviderSecret(row, clientSecret)
	if err != nil {
		return err
	}
	row.ClientSecret = sealed
	return m.store.AddProvider(row)
}

func splitScopes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}
