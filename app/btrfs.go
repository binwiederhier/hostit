package app

import (
	"log/slog"
	"path/filepath"
)

const (
	// snapshotsDirName holds read-only snapshots under the apps mount, one directory
	// per app. It sits beside the app subvolumes, not inside them, so a snapshot's
	// space is not charged to the app's own quota.
	snapshotsDirName = ".snapshots"
)

// btrfsEnabled reports whether the apps directory lives on a btrfs filesystem,
// which is what makes subvolume snapshots and hard qgroup quotas possible. The
// result is cached; on a plain ext4 host it is false and hostit keeps its previous
// directory-and-soft-quota behavior, so it still runs anywhere.
func (m *Manager) btrfsEnabled() bool {
	m.btrfsOnce.Do(func() {
		fstype, err := m.btrfs.Filesystem(m.config.AppsDir)
		m.btrfsOK = err == nil && fstype == "btrfs"
		// Logged once: if this is wrongly false, every app created this daemon
		// lifetime gets a plain-directory home instead of a subvolume (no
		// snapshots/quotas/rollback/fork), which is otherwise silent.
		slog.Info("Detected app-homes filesystem", "path", m.config.AppsDir, "btrfs", m.btrfsOK, "fstype", fstype, "stat_err", err)
	})
	return m.btrfsOK
}

// snapshotsRoot is where an app's snapshots live: <apps>/.snapshots/<id>/. Keyed
// on the app's id (like the home) so a rename does not move them.
func (m *Manager) snapshotsRoot(app string) string {
	return filepath.Join(m.config.AppsDir, snapshotsDirName, m.appID(app))
}

// snapshotPath is one snapshot's subvolume path.
func (m *Manager) snapshotPath(app, id string) string {
	return filepath.Join(m.snapshotsRoot(app), id)
}
