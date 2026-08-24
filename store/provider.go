package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A provider is a DEFINITION -- "here is how to connect to Acme" -- as opposed
// to a connection, which is one person's attached account.
//
// Three tiers hold them, and this table holds two of them:
//
//   - hostit's own catalog lives in Go (connections/providers.go).
//   - The OPERATOR's live in control.yml, or here with no owner. Everyone sees
//     them.
//   - A USER's live here with their own id. Only they see them.
//
// A user's own OAuth client is a perfectly ordinary thing to have: you register
// an app with the vendor, point it at hostit's callback, and paste the pair in.
// Nothing about OAuth requires the client to belong to the instance -- which is
// why this tier exists at all.

const (
	// ProviderOAuth is an OAuth 2.0 service. ProviderMCP is a named MCP server
	// offered so a user does not have to know its URL.
	ProviderOAuth = "oauth"
	ProviderMCP   = "mcp"

	providerIDPrefix = "pv_"
)

const (
	providerCols = `id, owner_id, name, label, kind, scopes, issuer, auth_url, token_url,
		client_id, client_secret, auth_params, long_lived, help, name_hint, url, created_at`
	insertProviderQuery = `
		INSERT INTO provider (` + providerCols + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	updateProviderQuery = `
		UPDATE provider SET label = ?, scopes = ?, issuer = ?, auth_url = ?, token_url = ?,
			client_id = ?, client_secret = ?, auth_params = ?, long_lived = ?,
			help = ?, name_hint = ?, url = ? WHERE id = ?
	`
	// Ordered so an INSTANCE definition (owner_id = '') is found before a
	// personal one of the same name: the operator's is what everybody else on
	// the instance already means by that word.
	selectProviderByNameQuery = `
		SELECT ` + providerCols + ` FROM provider
		WHERE name = ? AND (owner_id = '' OR owner_id = ?)
		ORDER BY owner_id ASC LIMIT 1
	`
	selectProvidersForQuery = `
		SELECT ` + providerCols + ` FROM provider
		WHERE owner_id = '' OR owner_id = ?
		ORDER BY kind, name
	`
	selectInstanceProvidersQuery = `SELECT ` + providerCols + ` FROM provider WHERE owner_id = '' ORDER BY kind, name`
	selectProviderQuery          = `SELECT ` + providerCols + ` FROM provider WHERE id = ?`
	deleteProviderQuery          = `DELETE FROM provider WHERE id = ?`
)

var (
	// ErrProviderNotFound means no such provider, or not one this caller sees.
	ErrProviderNotFound = errors.New("provider not found")
	// ErrProviderExists means this owner already defines that name.
	ErrProviderExists = errors.New("you already have a provider with that name")
)

// Provider is one provider definition.
type Provider struct {
	ID string
	// OwnerID is empty for an INSTANCE provider, which everyone sees.
	OwnerID string
	Name    string
	Label   string
	Kind    string
	Scopes  string

	// OAuth
	Issuer       string
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string // ciphertext, sealed before it reaches this layer
	AuthParams   string // JSON
	LongLived    bool

	// MCP
	URL string

	Help      string
	NameHint  string
	CreatedAt time.Time
}

// NewProviderID returns a fresh id. Callers that must seal a client secret
// against the row before inserting it need the id first (see
// connections.Binding), so it cannot be left to AddProvider.
func NewProviderID() string {
	return providerIDPrefix + randomID()
}

// AddProvider stores a new definition.
func (s *Store) AddProvider(p *Provider) error {
	if p.ID == "" {
		p.ID = providerIDPrefix + randomID()
	}
	_, err := s.db.Exec(insertProviderQuery, p.ID, p.OwnerID, p.Name, p.Label, p.Kind, p.Scopes,
		p.Issuer, p.AuthURL, p.TokenURL, p.ClientID, p.ClientSecret, p.AuthParams,
		boolToInt(p.LongLived), p.Help, p.NameHint, p.URL, p.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrProviderExists
	}
	return err
}

// UpdateProvider replaces everything but the name and the owner, which are what
// identify it.
func (s *Store) UpdateProvider(p *Provider) error {
	res, err := s.db.Exec(updateProviderQuery, p.Label, p.Scopes, p.Issuer, p.AuthURL, p.TokenURL,
		p.ClientID, p.ClientSecret, p.AuthParams, boolToInt(p.LongLived), p.Help, p.NameHint, p.URL, p.ID)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrProviderNotFound)
}

// ProviderByName resolves a name as this user sees it: their own definition, or
// the instance's, with the instance's winning.
func (s *Store) ProviderByName(userID, name string) (*Provider, error) {
	return scanOneProvider(s.db.QueryRow(selectProviderByNameQuery, name, userID))
}

// Provider returns one by id.
func (s *Store) Provider(id string) (*Provider, error) {
	return scanOneProvider(s.db.QueryRow(selectProviderQuery, id))
}

// ProvidersFor is everything this user can use: the instance's and their own.
func (s *Store) ProvidersFor(userID string) ([]*Provider, error) {
	return s.queryProviders(selectProvidersForQuery, userID)
}

// InstanceProviders is what the operator has defined for everyone.
func (s *Store) InstanceProviders() ([]*Provider, error) {
	return s.queryProviders(selectInstanceProvidersQuery)
}

// DeleteProvider forgets a definition. Connections already made with it keep
// their stored credential but can no longer be refreshed, which is why the
// caller warns first.
func (s *Store) DeleteProvider(id string) error {
	res, err := s.db.Exec(deleteProviderQuery, id)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrProviderNotFound)
}

func (s *Store) queryProviders(query string, args ...any) ([]*Provider, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Provider, 0)
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanOneProvider(row scanner) (*Provider, error) {
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProviderNotFound
	}
	return p, err
}

func scanProvider(row scanner) (*Provider, error) {
	var p Provider
	var longLived, createdAt int64
	if err := row.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Label, &p.Kind, &p.Scopes,
		&p.Issuer, &p.AuthURL, &p.TokenURL, &p.ClientID, &p.ClientSecret, &p.AuthParams,
		&longLived, &p.Help, &p.NameHint, &p.URL, &createdAt); err != nil {
		return nil, err
	}
	p.LongLived = longLived != 0
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}
