package node

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/workspace"
)

// exportSnapPrefix names the transient read-only snapshot an export archives.
// Dot-prefixed so the reconcile sweep skips it, and dropped as soon as the
// archive is written.
const exportSnapPrefix = ".export-"

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
		return nil, fmt.Errorf("workspace for %q is not on this node: %w", name, err)
	}
	snap := filepath.Join(m.config.AppsDir, exportSnapPrefix+randomExportID())
	if err := m.btrfs.Snapshot(subvol, snap, true, ""); err != nil {
		return nil, fmt.Errorf("cannot snapshot workspace for export: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		err := archive.Write(filepath.Join(snap, workspace.FilesDir), format, pw)
		_ = m.btrfs.DeleteSubvolume(snap)
		_ = pw.CloseWithError(err) // nil err is a clean EOF
	}()
	return pr, nil
}

func randomExportID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
