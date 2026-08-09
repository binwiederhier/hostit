// Package store persists the registry (apps, users, tokens, keys, settings) in
// a SQLite database, migrated in place on open (see migrate.go).
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	_ "modernc.org/sqlite" // SQLite driver (pure Go, no cgo)
)

const (
	// dbFileMode keeps the registry (tokens, session key) readable by root only
	dbFileMode = 0o600

	idLength = 12
)

// Store is the SQLite-backed registry
type Store struct {
	db *sql.DB
}

// NewStore opens (and if necessary creates or migrates) the database at filename
func NewStore(filename string) (*Store, error) {
	db, err := newRawDB(filename)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("cannot migrate database: %w", err)
	}
	if err := restrictDBFiles(filename); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// restrictDBFiles keeps the registry to root. It holds every app's agent token
// in the clear (so the app's page can show it again) and the session signing
// key, either of which is enough to impersonate a user.
func restrictDBFiles(filename string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(filename+suffix, dbFileMode); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// newRawDB opens the database without migrating it; tests use it to fabricate
// old schema versions.
//
// Background work (demo app startup, quota checks) hits the database
// concurrently with API requests, so use WAL plus a busy timeout, and serialize
// access with a single connection: SQLite writes are exclusive anyway, and at
// this scale one connection is plenty.
func newRawDB(filename string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filename+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Close closes the underlying database
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) queryDomains(query string, args ...any) ([]*Domain, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var domains []*Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// checkAffected turns a zero-row result into the given not-found error
func checkAffected(result sql.Result, notFound error) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return notFound
	}
	return nil
}

// NewAppID returns a fresh opaque app id. Callers that must know an app's id
// before inserting it -- the app's home directory is created id-named -- use this;
// AddApp otherwise assigns one itself.
func NewAppID() string {
	return randomID()
}

// randomID returns a random hex ID for users, tokens and keys
func randomID() string {
	b := make([]byte, idLength/2)
	if _, err := rand.Read(b); err != nil {
		panic(err) // Only fails if the system entropy source is broken
	}
	return hex.EncodeToString(b)
}
