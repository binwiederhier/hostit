package node

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

const (
	// exportSnapPrefix names the transient read-only snapshot an export archives.
	// Dot-prefixed so the reconcile sweep skips it, and dropped as soon as the
	// archive is written.
	exportSnapPrefix = ".export-"
	// exportSnapMaxAge is how long a transient export snapshot may live before the
	// sweep treats it as a crashed leftover. Generous, so a slow client streaming a
	// big workspace is never pulled out from under an in-flight export.
	exportSnapMaxAge = time.Hour
)

// ArchiveWorkspace streams the app's whole workspace (home/app) as an archive in
// the given format. It first takes a read-only btrfs snapshot, so the archive is
// a consistent point-in-time copy even while the app keeps writing, and drops
// the snapshot when the stream ends -- including when the reader is closed early
// (a client disconnect makes the archive write fail, which triggers the delete).
// The errors that CAN be reported -- the app missing, the snapshot failing --
// surface before any bytes are written; an error mid-stream closes the reader
// with it.
func (m *Machine) ArchiveWorkspace(name string, format archive.Format) (io.ReadCloser, error) {
	subvol := m.AppSubvolume(name)
	if _, err := os.Stat(subvol); err != nil {
		return nil, fmt.Errorf("workspace for %q is not on this node", name)
	}
	snap := filepath.Join(m.config.AppsDir, exportSnapPrefix+randomExportID())
	if err := m.btrfs.Snapshot(subvol, snap, true, ""); err != nil {
		return nil, fmt.Errorf("cannot snapshot workspace for export: %w", err)
	}
	return m.pipeArchive(filepath.Join(snap, workspace.FilesDir), format, func() {
		_ = m.btrfs.DeleteSubvolume(snap)
	}), nil
}

// ArchiveSnapshot streams an EXISTING snapshot's workspace (home/app) as an
// archive. A snapshot subvolume is already an immutable point-in-time copy, so
// unlike ArchiveWorkspace this takes no new snapshot and deletes nothing -- it
// just walks the snapshot's files straight into the stream.
func (m *Machine) ArchiveSnapshot(name, snapshotID string, format archive.Format) (io.ReadCloser, error) {
	// Resolve the id against the store first, exactly as delete/rollback do: it
	// both confirms the snapshot belongs to this app and keeps a crafted id (a
	// "../" traversal) from ever reaching filepath.Join below.
	rec, err := m.store.Snapshot(snapshotID)
	if err != nil {
		return nil, err
	}
	if rec.AppName != name {
		return nil, store.ErrSnapshotNotFound
	}
	snap := m.SnapshotPath(name, snapshotID)
	if _, err := os.Stat(snap); err != nil {
		return nil, fmt.Errorf("snapshot %q of %q is not on this node", snapshotID, name)
	}
	return m.pipeArchive(filepath.Join(snap, workspace.FilesDir), format, nil), nil
}

// pipeArchive writes the archive of dir into a pipe on a background goroutine and
// returns the read end. onDone, if set, runs after the archive is written (a
// client disconnect makes the write fail, which still runs it) -- the transient
// snapshot's cleanup hangs off it.
func (m *Machine) pipeArchive(dir string, format archive.Format, onDone func()) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		err := archive.Write(dir, format, pw)
		if onDone != nil {
			onDone()
		}
		_ = pw.CloseWithError(err) // nil err is a clean EOF
	}()
	return pr
}

func randomExportID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SweepExportSnapshots drops transient export snapshots (.export-*) a crashed
// export left behind. Each is normally deleted the moment its archive finishes;
// this is the backstop, so a leaked read-only snapshot cannot pin the workspace's
// old blocks forever. Only ones older than exportSnapMaxAge are removed.
func (m *Machine) SweepExportSnapshots() int {
	entries, err := os.ReadDir(m.config.AppsDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-exportSnapMaxAge)
	swept := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), exportSnapPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := m.btrfs.DeleteSubvolume(filepath.Join(m.config.AppsDir, e.Name())); err == nil {
			swept++
		}
	}
	return swept
}
