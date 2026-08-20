package store

import (
	"database/sql"
	"errors"
	"time"
)

// A connection is an account the OWNER connected once -- Google, GitHub, an
// IMAP mailbox -- which their apps can then be granted. It is per user and per
// provider: one Google connection, reusable across every app they own.
//
// Secret is what hostit has to keep: an OAuth refresh token, or a static
// credential the owner pasted. It is encrypted before it reaches this layer
// (see control/connections), so the column holds ciphertext.

const (
	// ConnectionOAuth stores a refresh token hostit exchanges for short-lived
	// access tokens; ConnectionStatic stores a credential used as-is (a
	// personal access token, an IMAP app password).
	ConnectionOAuth  = "oauth"
	ConnectionStatic = "static"
)

const (
	upsertConnectionQuery = `
		INSERT INTO connection (user_id, provider, kind, secret, scopes, meta, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, provider) DO UPDATE SET
			kind = excluded.kind, secret = excluded.secret,
			scopes = excluded.scopes, meta = excluded.meta, created_at = excluded.created_at
	`
	selectConnectionQuery = `
		SELECT user_id, provider, kind, secret, scopes, meta, created_at
		FROM connection WHERE user_id = ? AND provider = ?
	`
	selectConnectionsQuery = `
		SELECT user_id, provider, kind, secret, scopes, meta, created_at
		FROM connection WHERE user_id = ? ORDER BY provider
	`
	deleteConnectionQuery     = `DELETE FROM connection WHERE user_id = ? AND provider = ?`
	deleteGrantsByProvider    = `DELETE FROM app_connection WHERE provider = ? AND app_id IN (SELECT id FROM app WHERE owner_id = ?)`
	insertGrantQuery          = `INSERT OR IGNORE INTO app_connection (app_id, provider) VALUES (?, ?)`
	deleteGrantQuery          = `DELETE FROM app_connection WHERE app_id = ? AND provider = ?`
	selectAppConnectionsQuery = `SELECT provider FROM app_connection WHERE app_id = ? ORDER BY provider`
	deleteGrantsByAppIDQuery  = `DELETE FROM app_connection WHERE app_id = ?`
)

// ErrConnectionNotFound means this user has not connected that provider.
var ErrConnectionNotFound = errors.New("connection not found")

// Connection is one owner-connected account.
type Connection struct {
	UserID    string
	Provider  string
	Kind      string
	Secret    string // ciphertext: a refresh token, or a static credential
	Scopes    string
	Meta      string // provider-specific, non-secret (an IMAP host, an account email)
	CreatedAt time.Time
}

// SaveConnection stores or replaces a user's connection to a provider.
// Reconnecting replaces rather than accumulates: an owner who re-runs consent
// expects one Google connection, not a pile of them.
func (s *Store) SaveConnection(c *Connection) error {
	_, err := s.db.Exec(upsertConnectionQuery, c.UserID, c.Provider, c.Kind, c.Secret, c.Scopes, c.Meta, c.CreatedAt.Unix())
	return err
}

// Connection returns one user's connection to a provider.
func (s *Store) Connection(userID, provider string) (*Connection, error) {
	var c Connection
	var created int64
	err := s.db.QueryRow(selectConnectionQuery, userID, provider).
		Scan(&c.UserID, &c.Provider, &c.Kind, &c.Secret, &c.Scopes, &c.Meta, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConnectionNotFound
	} else if err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(created, 0)
	return &c, nil
}

// Connections lists everything a user has connected.
func (s *Store) Connections(userID string) ([]*Connection, error) {
	rows, err := s.db.Query(selectConnectionsQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Connection
	for rows.Next() {
		var c Connection
		var created int64
		if err := rows.Scan(&c.UserID, &c.Provider, &c.Kind, &c.Secret, &c.Scopes, &c.Meta, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// DeleteConnection disconnects a provider and drops every grant of it the
// user's apps held. Leaving the grants would quietly restore an app's access
// the moment the owner reconnected.
func (s *Store) DeleteConnection(userID, provider string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(deleteGrantsByProvider, provider, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(deleteConnectionQuery, userID, provider); err != nil {
		return err
	}
	return tx.Commit()
}

// GrantConnection lets one app use one of its owner's connections.
func (s *Store) GrantConnection(appID, provider string) error {
	_, err := s.db.Exec(insertGrantQuery, appID, provider)
	return err
}

// RevokeConnection takes that grant away.
func (s *Store) RevokeConnection(appID, provider string) error {
	_, err := s.db.Exec(deleteGrantQuery, appID, provider)
	return err
}

// AppConnections is what this app has been granted -- never everything its
// owner has connected, which is the point of the per-app grant.
func (s *Store) AppConnections(appID string) ([]string, error) {
	rows, err := s.db.Query(selectAppConnectionsQuery, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
