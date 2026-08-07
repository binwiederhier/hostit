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
	"strings"
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

	selectAssistantSessionQuery = `SELECT transcript FROM assistant_session WHERE app_name = ?`
	upsertAssistantSessionQuery = `
		INSERT INTO assistant_session (app_name, transcript, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(app_name) DO UPDATE SET transcript = excluded.transcript, updated_at = excluded.updated_at
	`
	deleteAssistantSessionQuery = `DELETE FROM assistant_session WHERE app_name = ?`

	insertSnapshotQuery     = `INSERT INTO snapshot (id, app_name, label, created_at, auto) VALUES (?, ?, ?, ?, ?)`
	selectSnapshotsQuery    = `SELECT id, app_name, label, created_at, auto FROM snapshot WHERE app_name = ? ORDER BY created_at DESC`
	selectSnapshotQuery     = `SELECT id, app_name, label, created_at, auto FROM snapshot WHERE id = ?`
	deleteSnapshotQuery     = `DELETE FROM snapshot WHERE id = ?`
	deleteAppSnapshotsQuery = `DELETE FROM snapshot WHERE app_name = ?`

	domainCols              = `domain, app_name, status, last_error, created_at, active_at`
	insertDomainQuery       = `INSERT INTO app_domain (domain, app_name, status, last_error, created_at) VALUES (?, ?, ?, '', ?)`
	selectDomainsByAppQuery = `SELECT ` + domainCols + ` FROM app_domain WHERE app_name = ? ORDER BY created_at`
	selectDomainQuery       = `SELECT ` + domainCols + ` FROM app_domain WHERE domain = ?`
	selectAllDomainsQuery   = `SELECT ` + domainCols + ` FROM app_domain`
	updateDomainStatusQuery = `UPDATE app_domain SET status = ?, last_error = ?, active_at = ? WHERE domain = ?`
	deleteDomainQuery       = `DELETE FROM app_domain WHERE domain = ?`
	deleteAppDomainsQuery   = `DELETE FROM app_domain WHERE app_name = ?`
)

var (
	// ErrAppNotFound is returned when an app does not exist in the registry
	ErrAppNotFound = errors.New("app not found")
	// ErrSnapshotNotFound is returned when a snapshot id does not exist
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrAppDomainNotFound is returned when a custom app domain does not exist
	ErrAppDomainNotFound = errors.New("domain not found")
	// ErrAppDomainExists is returned when a domain is already registered to an app
	ErrAppDomainExists = errors.New("domain already in use")
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
	if _, err := s.db.Exec(deleteAssistantSessionQuery, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppSnapshotsQuery, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppDomainsQuery, name); err != nil {
		return err
	}
	return s.RemoveTokensByApp(name)
}

// LoadAssistantSession returns the app's stored assistant transcript (a JSON
// blob), or an empty string if there is none yet
func (s *Store) LoadAssistantSession(appName string) (string, error) {
	var transcript string
	err := s.db.QueryRow(selectAssistantSessionQuery, appName).Scan(&transcript)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return transcript, err
}

// SaveAssistantSession stores (upserts) the app's assistant transcript
func (s *Store) SaveAssistantSession(appName, transcript string) error {
	_, err := s.db.Exec(upsertAssistantSessionQuery, appName, transcript, time.Now().Unix())
	return err
}

// DeleteAssistantSession forgets the app's assistant transcript
func (s *Store) DeleteAssistantSession(appName string) error {
	_, err := s.db.Exec(deleteAssistantSessionQuery, appName)
	return err
}

// AddSnapshot records a new snapshot's metadata
func (s *Store) AddSnapshot(snap *Snapshot) error {
	_, err := s.db.Exec(insertSnapshotQuery, snap.ID, snap.AppName, snap.Label, snap.CreatedAt.Unix(), boolToInt(snap.Auto))
	return err
}

// Snapshots lists an app's snapshots, newest first
func (s *Store) Snapshots(appName string) ([]*Snapshot, error) {
	rows, err := s.db.Query(selectSnapshotsQuery, appName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snaps []*Snapshot
	for rows.Next() {
		snap, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, rows.Err()
}

// Snapshot returns one snapshot by id, or ErrSnapshotNotFound
func (s *Store) Snapshot(id string) (*Snapshot, error) {
	snap, err := scanSnapshot(s.db.QueryRow(selectSnapshotQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSnapshotNotFound
	}
	return snap, err
}

// DeleteSnapshot forgets one snapshot's metadata (the subvolume is removed by the
// caller)
func (s *Store) DeleteSnapshot(id string) error {
	_, err := s.db.Exec(deleteSnapshotQuery, id)
	return err
}

// AddDomain registers a custom domain for an app, or ErrAppDomainExists if the
// domain already belongs to some app
func (s *Store) AddDomain(d *Domain) error {
	_, err := s.db.Exec(insertDomainQuery, d.Domain, d.AppName, string(d.Status), d.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrAppDomainExists
	}
	return err
}

// Domains lists an app's custom domains, oldest first
func (s *Store) Domains(appName string) ([]*Domain, error) {
	return s.queryDomains(selectDomainsByAppQuery, appName)
}

// AllDomains lists every custom domain, for building the routing cache and TLS
// management at startup
func (s *Store) AllDomains() ([]*Domain, error) {
	return s.queryDomains(selectAllDomainsQuery)
}

// Domain returns one domain by hostname, or ErrAppDomainNotFound
func (s *Store) Domain(domain string) (*Domain, error) {
	d, err := scanDomain(s.db.QueryRow(selectDomainQuery, domain))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppDomainNotFound
	}
	return d, err
}

// SetDomainStatus updates a domain's issuance state; activeAt is nil unless the
// domain just became active
func (s *Store) SetDomainStatus(domain string, status DomainStatus, lastErr string, activeAt *time.Time) error {
	var active *int64
	if activeAt != nil {
		unix := activeAt.Unix()
		active = &unix
	}
	res, err := s.db.Exec(updateDomainStatusQuery, string(status), lastErr, active, domain)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrAppDomainNotFound)
}

// DeleteDomain forgets a custom domain (the caller drops its certificate)
func (s *Store) DeleteDomain(domain string) error {
	_, err := s.db.Exec(deleteDomainQuery, domain)
	return err
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

func scanDomain(row scanner) (*Domain, error) {
	var d Domain
	var status string
	var createdAt int64
	var activeAt *int64
	if err := row.Scan(&d.Domain, &d.AppName, &status, &d.LastError, &createdAt, &activeAt); err != nil {
		return nil, err
	}
	d.Status = DomainStatus(status)
	d.CreatedAt = time.Unix(createdAt, 0)
	if activeAt != nil {
		t := time.Unix(*activeAt, 0)
		d.ActiveAt = &t
	}
	return &d, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSnapshot(row scanner) (*Snapshot, error) {
	var snap Snapshot
	var createdAt int64
	var auto int
	if err := row.Scan(&snap.ID, &snap.AppName, &snap.Label, &createdAt, &auto); err != nil {
		return nil, err
	}
	snap.CreatedAt = time.Unix(createdAt, 0)
	snap.Auto = auto != 0
	return &snap, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
