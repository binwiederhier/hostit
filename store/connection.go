package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A connection is something the OWNER attached once -- a Google Calendar, a
// Slack workspace, an IMAP mailbox, a pasted API key -- which their apps can
// then be granted. It belongs to a user, not to an app, so it is reusable
// across every app they own.
//
// It is keyed on an id with an owner-chosen SLUG beside it, deliberately: one
// person can hold several of the same provider (a work calendar and a personal
// one) and an app asks for the one it wants by slug. A grant therefore names a
// connection, never a provider -- granting the work calendar must not quietly
// hand over the personal one.
//
// Secret is what hostit has to keep: an OAuth refresh token, or a credential
// the owner pasted. It is encrypted before it reaches this layer (see the
// connections package), so the column holds ciphertext.

const (
	// ConnectionOAuth stores a refresh token hostit exchanges for short-lived
	// access tokens; ConnectionStatic stores a credential used as-is (a
	// personal access token, an IMAP app password, an API key).
	ConnectionOAuth  = "oauth"
	ConnectionStatic = "static"

	connectionIDPrefix = "cn_"
)

const (
	connectionCols        = `id, user_id, slug, provider, kind, label, secret, scopes, meta, created_at`
	insertConnectionQuery = `
		INSERT INTO connection (` + connectionCols + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	selectConnectionQuery           = `SELECT ` + connectionCols + ` FROM connection WHERE id = ?`
	selectConnectionBySlugQuery     = `SELECT ` + connectionCols + ` FROM connection WHERE user_id = ? AND slug = ?`
	selectConnectionsQuery          = `SELECT ` + connectionCols + ` FROM connection WHERE user_id = ? ORDER BY provider, slug`
	updateConnectionSecretQuery     = `UPDATE connection SET secret = ?, scopes = ?, meta = ? WHERE id = ?`
	renameConnectionQuery           = `UPDATE connection SET slug = ?, label = ? WHERE id = ?`
	deleteConnectionQuery           = `DELETE FROM connection WHERE id = ?`
	selectAllConnectionsQuery       = `SELECT ` + connectionCols + ` FROM connection ORDER BY user_id, slug`
	updateConnectionSecretOnlyQuery = `UPDATE connection SET secret = ? WHERE id = ?`

	insertGrantQuery          = `INSERT OR IGNORE INTO app_connection (app_id, connection_id, created_at) VALUES (?, ?, ?)`
	deleteGrantQuery          = `DELETE FROM app_connection WHERE app_id = ? AND connection_id = ?`
	deleteGrantsByConnection  = `DELETE FROM app_connection WHERE connection_id = ?`
	deleteGrantsByAppIDQuery  = `DELETE FROM app_connection WHERE app_id = ?`
	countGrantsQuery          = `SELECT COUNT(*) FROM app_connection WHERE connection_id = ?`
	selectAppConnectionsQuery = `
		SELECT ` + connectionCols + ` FROM connection
		WHERE id IN (SELECT connection_id FROM app_connection WHERE app_id = ?)
		ORDER BY provider, slug
	`
)

var (
	// ErrConnectionNotFound means no such connection, or not this owner's.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrConnectionSlugExists means this owner already uses that slug. Slugs are
	// how an app names a connection, so they have to be unique per owner.
	ErrConnectionSlugExists = errors.New("you already have a connection with that name")
)

// Connection is one attached account or credential.
type Connection struct {
	ID        string
	UserID    string
	Slug      string // owner-chosen, unique per owner; how an app addresses it
	Provider  string
	Kind      string
	Label     string // free text for the UI; the slug is the identifier
	Secret    string // ciphertext: a refresh token, or a static credential
	Scopes    string
	Meta      string // provider-specific, non-secret (an IMAP host, an account email)
	CreatedAt time.Time
}

// NewConnectionID returns a fresh id. Callers that must seal a credential
// against the row before inserting it need the id first (see
// connections.Binding), so it cannot be left to AddConnection.
func NewConnectionID() string {
	return connectionIDPrefix + randomID()
}

// AddConnection stores a new connection, assigning an id if it has none.
func (s *Store) AddConnection(c *Connection) error {
	if c.ID == "" {
		c.ID = connectionIDPrefix + randomID()
	}
	_, err := s.db.Exec(insertConnectionQuery, c.ID, c.UserID, c.Slug, c.Provider, c.Kind, c.Label,
		c.Secret, c.Scopes, c.Meta, c.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrConnectionSlugExists
	}
	return err
}

// Connection returns one by id.
func (s *Store) Connection(id string) (*Connection, error) {
	return scanOneConnection(s.db.QueryRow(selectConnectionQuery, id))
}

// ConnectionBySlug returns one of an owner's connections by the name they gave
// it. Scoped to the owner, so one person's slug never resolves for another.
func (s *Store) ConnectionBySlug(userID, slug string) (*Connection, error) {
	return scanOneConnection(s.db.QueryRow(selectConnectionBySlugQuery, userID, slug))
}

// Connections lists everything an owner has attached.
func (s *Store) Connections(userID string) ([]*Connection, error) {
	return s.queryConnections(selectConnectionsQuery, userID)
}

// AllConnections lists every connection on the instance, for key rotation. The
// only caller that legitimately reads across owners.
func (s *Store) AllConnections() ([]*Connection, error) {
	return s.queryConnections(selectAllConnectionsQuery)
}

// UpdateConnectionSecretOnly replaces the ciphertext and nothing else, which is
// what rotation needs: scopes and meta are not re-derived, only re-keyed.
func (s *Store) UpdateConnectionSecretOnly(id, secret string) error {
	res, err := s.db.Exec(updateConnectionSecretOnlyQuery, secret, id)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrConnectionNotFound)
}

// AppConnections lists the connections an app has been granted.
func (s *Store) AppConnections(appID string) ([]*Connection, error) {
	return s.queryConnections(selectAppConnectionsQuery, appID)
}

// UpdateConnectionSecret replaces the stored credential, which is what a
// re-consent does: same connection, same slug, fresh secret.
func (s *Store) UpdateConnectionSecret(id, secret, scopes, meta string) error {
	res, err := s.db.Exec(updateConnectionSecretQuery, secret, scopes, meta, id)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrConnectionNotFound)
}

// RenameConnection changes the slug an app addresses it by, and the label a
// person reads. Renaming breaks any app configured for the old slug, which is
// the owner's call to make.
func (s *Store) RenameConnection(id, slug, label string) error {
	res, err := s.db.Exec(renameConnectionQuery, slug, label, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConnectionSlugExists
		}
		return err
	}
	return checkAffected(res, ErrConnectionNotFound)
}

// DeleteConnection forgets a connection and every grant naming it, so
// disconnecting cuts every app off at once rather than leaving them holding a
// grant to something that no longer exists.
func (s *Store) DeleteConnection(id string) error {
	if _, err := s.db.Exec(deleteGrantsByConnection, id); err != nil {
		return err
	}
	res, err := s.db.Exec(deleteConnectionQuery, id)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrConnectionNotFound)
}

// GrantConnection lets one app use one connection.
func (s *Store) GrantConnection(appID, connectionID string) error {
	_, err := s.db.Exec(insertGrantQuery, appID, connectionID, time.Now().Unix())
	return err
}

// RevokeConnection takes the grant away, leaving the connection itself alone.
func (s *Store) RevokeConnection(appID, connectionID string) error {
	_, err := s.db.Exec(deleteGrantQuery, appID, connectionID)
	return err
}

// CountGrants is how many apps hold a connection; used to warn before a
// disconnect, and by tests to prove a cascade actually cascaded.
func (s *Store) CountGrants(connectionID string) (int, error) {
	var n int
	err := s.db.QueryRow(countGrantsQuery, connectionID).Scan(&n)
	return n, err
}

func (s *Store) queryConnections(query string, args ...any) ([]*Connection, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Connection, 0)
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanOneConnection(row scanner) (*Connection, error) {
	c, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConnectionNotFound
	}
	return c, err
}

func scanConnection(row scanner) (*Connection, error) {
	var c Connection
	var createdAt int64
	if err := row.Scan(&c.ID, &c.UserID, &c.Slug, &c.Provider, &c.Kind, &c.Label,
		&c.Secret, &c.Scopes, &c.Meta, &createdAt); err != nil {
		return nil, err
	}
	c.CreatedAt = time.Unix(createdAt, 0)
	return &c, nil
}
