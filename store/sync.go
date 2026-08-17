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
	// deleteForeignSnapshotsQuery drops the snapshots of apps the payload does
	// not list -- an app that left this node takes its records with it. Records
	// for apps that ARE listed are kept even when the payload omits them: see
	// ReplaceNodeMirror.
	deleteForeignSnapshotsQuery = `DELETE FROM snapshot WHERE app_name NOT IN (SELECT name FROM app)`
	deleteAllAppsQuery          = `DELETE FROM app`
	selectAppIDByNameQuery      = `SELECT id FROM app WHERE name = ?`
	replaceMirrorAppQuery       = `
		INSERT INTO app (id, name, port, host, owner_id, disk_mb, created_at, image_tag, uid, powered_off)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	// OR REPLACE: the node's snapshot rows are merged rather than cleared
	// first, so a record the payload also carries is updated in place.
	replaceMirrorSnapshotQuery = `
		INSERT OR REPLACE INTO snapshot (id, app_name, app_id, label, created_at, auto)
		VALUES (?, ?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?, ?, ?)`
)

// ReplaceNodeMirror swaps the mirror's app rows for the pushed state and merges
// its snapshot rows in, atomically; full-row fidelity, since the node's loops
// read all of it.
//
// Apps are replaced outright: control is their only author. Snapshots are NOT,
// because for a moment the node knows something control does not -- control
// builds a payload by reading its registry and sends it afterwards, so a
// snapshot taken in between is newer than the payload that omits it, and
// wholesale replacement deleted the record of a snapshot the user had just
// taken. Absence therefore means nothing here: control deletes a snapshot by
// telling the node to (Manager.PruneSnapshots), never by leaving it out.
func (s *Store) ReplaceNodeMirror(apps []*App, snaps []*Snapshot) error {
	return s.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(deleteAllAppsQuery); err != nil {
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
		// Now that the app rows are the payload's, this drops the snapshots of
		// apps that are no longer hosted here.
		if _, err := tx.Exec(deleteForeignSnapshotsQuery); err != nil {
			return err
		}
		return nil
	})
}

// ReplaceAppSnapshots swaps ONE app's snapshot rows for the node-reported
// authoritative list, leaving every other app's rows alone.
func (s *Store) ReplaceAppSnapshots(appName string, snaps []*Snapshot) error {
	return s.inTx(func(tx *sql.Tx) error {
		var appID string
		if err := tx.QueryRow(selectAppIDByNameQuery, appName).Scan(&appID); err != nil {
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
