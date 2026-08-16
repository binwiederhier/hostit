package store

import (
	"database/sql"
	"errors"
	"time"
)

const (
	// snapshotName resolves the app's current name from its id, so a renamed app's
	// snapshots still report the right owner; snapshots are keyed on app_id.
	snapshotName            = `COALESCE((SELECT name FROM app a WHERE a.id = snapshot.app_id), snapshot.app_name)`
	insertSnapshotQuery     = `INSERT INTO snapshot (id, app_name, app_id, label, created_at, auto) VALUES (?, ?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?, ?, ?)`
	selectSnapshotsQuery    = `SELECT id, ` + snapshotName + `, label, created_at, auto FROM snapshot WHERE app_id = (SELECT id FROM app WHERE name = ?) ORDER BY created_at DESC`
	selectSnapshotQuery     = `SELECT id, ` + snapshotName + `, label, created_at, auto FROM snapshot WHERE id = ?`
	selectAllSnapshotsQuery = `SELECT id, ` + snapshotName + `, label, created_at, auto FROM snapshot ORDER BY created_at DESC`
	deleteSnapshotQuery     = `DELETE FROM snapshot WHERE id = ?`
	deleteAppSnapshotsQuery = `DELETE FROM snapshot WHERE app_id = ? OR (app_id = '' AND app_name = ?)`
)

var (
	// ErrSnapshotNotFound is returned when a snapshot id does not exist
	ErrSnapshotNotFound = errors.New("snapshot not found")
)

// AddSnapshot records a new snapshot's metadata
func (s *Store) AddSnapshot(snap *Snapshot) error {
	_, err := s.db.Exec(insertSnapshotQuery, snap.ID, snap.AppName, snap.AppName, snap.Label, snap.CreatedAt.Unix(), boolToInt(snap.Auto))
	return err
}

// AllSnapshots lists every snapshot row, newest first. On a node this is its
// mirror -- the records for the apps it hosts -- which control reads back on
// rejoin.
func (s *Store) AllSnapshots() ([]*Snapshot, error) {
	rows, err := s.db.Query(selectAllSnapshotsQuery)
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
