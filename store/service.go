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
	"time"

	_ "modernc.org/sqlite" // SQLite driver (pure Go, no cgo)
)

const (
	// dbFileMode keeps the registry (tokens, session key) readable by root only
	dbFileMode = 0o600

	insertAppQuery = `INSERT INTO app (name, port, host, owner_id, created_at) VALUES (?, ?, ?, ?, ?)`
	selectAppQuery = `
		SELECT name, port, host, owner_id, disk_mb, over_quota, created_at
		FROM app WHERE name = ?
	`
	selectAppsQuery = `
		SELECT name, port, host, owner_id, disk_mb, over_quota, created_at
		FROM app ORDER BY name
	`
	selectAppsByOwnerQuery = `
		SELECT name, port, host, owner_id, disk_mb, over_quota, created_at
		FROM app WHERE owner_id = ? ORDER BY name
	`
	selectAppCountByOwnerQuery = `SELECT COUNT(*) FROM app WHERE owner_id = ?`
	selectPortsQuery           = `SELECT port FROM app ORDER BY port`
	updateAppUsageQuery        = `UPDATE app SET disk_mb = ?, over_quota = ? WHERE name = ?`
	deleteAppQuery             = `DELETE FROM app WHERE name = ?`
)

var (
	// ErrAppNotFound is returned when an app does not exist in the registry
	ErrAppNotFound = errors.New("app not found")
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

// AddApp inserts a new app; name and port must be unique
func (s *Store) AddApp(app *App) error {
	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertAppQuery, app.Name, app.Port, app.Host, app.OwnerID, app.CreatedAt.Unix())
	return err
}

// App returns the app with the given name, or ErrAppNotFound
func (s *Store) App(name string) (*App, error) {
	var app App
	var createdAt int64
	err := s.db.QueryRow(selectAppQuery, name).Scan(&app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &app.OverQuota, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	} else if err != nil {
		return nil, err
	}
	app.CreatedAt = time.Unix(createdAt, 0)
	return &app, nil
}

// Apps returns all registered apps, sorted by name
func (s *Store) Apps() ([]*App, error) {
	return s.queryApps(selectAppsQuery)
}

// AppsByOwner returns the apps owned by a user, sorted by name
func (s *Store) AppsByOwner(ownerID string) ([]*App, error) {
	return s.queryApps(selectAppsByOwnerQuery, ownerID)
}

// AppCountByOwner counts a user's apps, for limit enforcement
func (s *Store) AppCountByOwner(ownerID string) (int, error) {
	var count int
	err := s.db.QueryRow(selectAppCountByOwnerQuery, ownerID).Scan(&count)
	return count, err
}

// UsedPorts returns all allocated ports, sorted ascending
func (s *Store) UsedPorts() ([]int, error) {
	rows, err := s.db.Query(selectPortsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ports := make([]int, 0)
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return nil, err
		}
		ports = append(ports, port)
	}
	return ports, rows.Err()
}

// UpdateAppUsage records measured disk usage and whether the app is over quota
func (s *Store) UpdateAppUsage(name string, diskMB int, overQuota bool) error {
	result, err := s.db.Exec(updateAppUsageQuery, diskMB, overQuota, name)
	if err != nil {
		return err
	}
	return checkAffected(result, ErrAppNotFound)
}

// RemoveApp deletes the app with the given name, or returns ErrAppNotFound
func (s *Store) RemoveApp(name string) error {
	result, err := s.db.Exec(deleteAppQuery, name)
	if err != nil {
		return err
	}
	if err := checkAffected(result, ErrAppNotFound); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppKeysQuery, name); err != nil {
		return err
	}
	return s.RemoveTokensByApp(name)
}

// Close closes the underlying database
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) queryApps(query string, args ...any) ([]*App, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]*App, 0)
	for rows.Next() {
		var app App
		var createdAt int64
		if err := rows.Scan(&app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &app.OverQuota, &createdAt); err != nil {
			return nil, err
		}
		app.CreatedAt = time.Unix(createdAt, 0)
		apps = append(apps, &app)
	}
	return apps, rows.Err()
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

// randomID returns a random hex ID for users, tokens and keys
func randomID() string {
	b := make([]byte, idLength/2)
	if _, err := rand.Read(b); err != nil {
		panic(err) // Only fails if the system entropy source is broken
	}
	return hex.EncodeToString(b)
}
