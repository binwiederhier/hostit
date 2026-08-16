package store

import (
	"database/sql"
)

// Node-mirror sync: in the service split, hostit-node keeps its own SQLite
// with a MIRROR of the app (and snapshot) rows it hosts, pushed by control on
// connect and after every registry mutation. ReplaceNodeMirror is the node's
// receiving half; ReplaceAppSnapshots is control's receiving half for the
// node-originated snapshot callback (auto-snapshots, retention pruning).

const (
	replaceMirrorAppQuery = `
		INSERT INTO app (id, name, port, host, owner_id, disk_mb, created_at, image_tag, uid, powered_off)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	replaceMirrorSnapshotQuery = `
		INSERT INTO snapshot (id, app_name, app_id, label, created_at, auto)
		VALUES (?, ?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?, ?, ?)`
)

// ReplaceNodeMirror swaps the mirror's app and snapshot rows for the pushed
// state, atomically; full-row fidelity, since the node's loops read all of it.
func (s *Store) ReplaceNodeMirror(apps []*App, snaps []*Snapshot) error {
	return s.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM snapshot`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM app`); err != nil {
			return err
		}
		for _, a := range apps {
			if _, err := tx.Exec(replaceMirrorAppQuery, a.ID, a.Name, a.Port, a.Host, a.OwnerID, a.DiskMB, a.CreatedAt.Unix(), a.ImageTag, a.UID, boolToInt(a.PoweredOff)); err != nil {
				return err
			}
		}
		for _, snap := range snaps {
			if _, err := tx.Exec(replaceMirrorSnapshotQuery, snap.ID, snap.AppName, snap.AppName, snap.Label, snap.CreatedAt.Unix(), boolToInt(snap.Auto)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceAppSnapshots swaps ONE app's snapshot rows for the node-reported
// authoritative list, leaving every other app's rows alone.
func (s *Store) ReplaceAppSnapshots(appName string, snaps []*Snapshot) error {
	return s.inTx(func(tx *sql.Tx) error {
		var appID string
		if err := tx.QueryRow(`SELECT id FROM app WHERE name = ?`, appName).Scan(&appID); err != nil {
			return err
		}
		if _, err := tx.Exec(deleteAppSnapshotsQuery, appID, appName); err != nil {
			return err
		}
		for _, snap := range snaps {
			if _, err := tx.Exec(replaceMirrorSnapshotQuery, snap.ID, snap.AppName, snap.AppName, snap.Label, snap.CreatedAt.Unix(), boolToInt(snap.Auto)); err != nil {
				return err
			}
		}
		return nil
	})
}

// inTx runs fn in one transaction, rolling back on error.
func (s *Store) inTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
