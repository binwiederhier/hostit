package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	nodeCols            = `name, address, joined_at, last_seen`
	insertNodeQuery     = `INSERT INTO node (name, address, token_hash, token_expires_at, joined_at) VALUES (?, ?, ?, ?, 0)`
	remintNodeQuery     = `UPDATE node SET address = ?, token_hash = ?, token_expires_at = ? WHERE name = ? AND joined_at = 0`
	ensureNodeQuery     = `INSERT INTO node (name, address, token_hash, token_expires_at, joined_at) VALUES (?, ?, '', 0, ?) ON CONFLICT (name) DO UPDATE SET address = excluded.address`
	selectNodeQuery     = `SELECT ` + nodeCols + ` FROM node WHERE name = ?`
	selectNodesQuery    = `SELECT ` + nodeCols + ` FROM node ORDER BY name`
	consumeTokenQuery   = `UPDATE node SET token_hash = '', token_expires_at = 0, joined_at = ? WHERE token_hash = ? AND token_hash != '' AND token_expires_at > ? RETURNING name`
	updateNodeSeenQuery = `UPDATE node SET last_seen = ? WHERE name = ?`
	deleteNodeQuery     = `DELETE FROM node WHERE name = ?`
)

var (
	// ErrNodeNotFound is returned when a node name is not registered
	ErrNodeNotFound = errors.New("node not found")
	// ErrNodeExists is returned when adding a node name that already joined
	ErrNodeExists = errors.New("node already joined")
	// ErrNodeJoinTokenInvalid is returned for an unknown, used or expired join token
	ErrNodeJoinTokenInvalid = errors.New("join token invalid or expired")
)

// CreateNode registers a pending node with its (hashed) one-time join token.
// Re-creating a node that never joined replaces its token (lost-token UX);
// a node that already joined must be removed first.
func (s *Store) CreateNode(name, address, tokenHash string, tokenExpiresAt time.Time) error {
	res, err := s.db.Exec(remintNodeQuery, address, tokenHash, tokenExpiresAt.Unix(), name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	_, err = s.db.Exec(insertNodeQuery, name, address, tokenHash, tokenExpiresAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrNodeExists
	}
	return err
}

// EnsureNode upserts a node that needs no enrollment -- the colocated "local"
// node, joined from the start; safe to call at every control start.
func (s *Store) EnsureNode(name, address string) error {
	_, err := s.db.Exec(ensureNodeQuery, name, address, time.Now().Unix())
	return err
}

// ConsumeNodeJoinToken burns an unexpired token and marks its node joined,
// returning the node name; single-use by construction (the hash is cleared in
// the same statement that matches it).
func (s *Store) ConsumeNodeJoinToken(tokenHash string, now time.Time) (string, error) {
	var name string
	err := s.db.QueryRow(consumeTokenQuery, now.Unix(), tokenHash, now.Unix()).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNodeJoinTokenInvalid
	}
	return name, err
}

// Node returns one node by name, or ErrNodeNotFound.
func (s *Store) Node(name string) (*Node, error) {
	row := s.db.QueryRow(selectNodeQuery, name)
	return scanNode(row)
}

// Nodes returns all registered nodes (joined and pending), sorted by name.
func (s *Store) Nodes() ([]*Node, error) {
	rows, err := s.db.Query(selectNodesQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]*Node, 0)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// SetNodeSeen records a heartbeat/connect timestamp for liveness display and
// placement decisions.
func (s *Store) SetNodeSeen(name string, seen time.Time) error {
	_, err := s.db.Exec(updateNodeSeenQuery, seen.Unix(), name)
	return err
}

// RemoveNode unregisters a node; its certificate stops being accepted because
// registration is checked at connect time.
func (s *Store) RemoveNode(name string) error {
	_, err := s.db.Exec(deleteNodeQuery, name)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNode(row rowScanner) (*Node, error) {
	var joinedAt, lastSeen int64
	n := &Node{}
	if err := row.Scan(&n.Name, &n.Address, &joinedAt, &lastSeen); err != nil {
		return nil, ErrNodeNotFound
	}
	if joinedAt > 0 {
		n.JoinedAt = time.Unix(joinedAt, 0)
	}
	if lastSeen > 0 {
		n.LastSeen = time.Unix(lastSeen, 0)
	}
	return n, nil
}
