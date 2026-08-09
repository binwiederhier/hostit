package app

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// snapshotsDirName holds read-only snapshots under the apps mount, one directory
	// per app. It sits beside the app subvolumes, not inside them, so a snapshot's
	// space is not charged to the app's own quota.
	snapshotsDirName = ".snapshots"
	// btrfsTimeout bounds a btrfs command; these are metadata operations (create,
	// snapshot, qgroup) and return quickly, but must never wedge a request.
	btrfsTimeout = 30 * time.Second
)

// btrfsEnabled reports whether the apps directory lives on a btrfs filesystem,
// which is what makes subvolume snapshots and hard qgroup quotas possible. The
// result is cached; on a plain ext4 host it is false and hostit keeps its previous
// directory-and-soft-quota behavior, so it still runs anywhere.
func (m *Manager) btrfsEnabled() bool {
	m.btrfsOnce.Do(func() {
		out, err := m.runner.RunTimeout(btrfsTimeout, "stat", "-f", "-c", "%T", m.config.AppsDir)
		m.btrfsOK = err == nil && strings.TrimSpace(out) == "btrfs"
		// Logged once: if this is wrongly false, every app created this daemon
		// lifetime gets a plain-directory home instead of a subvolume (no
		// snapshots/quotas/rollback/fork), which is otherwise silent.
		slog.Info("Detected app-homes filesystem", "path", m.config.AppsDir, "btrfs", m.btrfsOK, "fstype", strings.TrimSpace(out), "stat_err", err)
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

// createSubvolume makes a new btrfs subvolume at path (the app's home).
func (m *Manager) createSubvolume(path string) error {
	_, err := m.runner.RunTimeout(btrfsTimeout, "btrfs", "subvolume", "create", path)
	return err
}

// deleteSubvolume removes a subvolume (an app home or a snapshot). A read-only
// snapshot must have its flag cleared first, which the caller handles.
func (m *Manager) deleteSubvolume(path string) error {
	_, err := m.runner.RunTimeout(btrfsTimeout, "btrfs", "subvolume", "delete", path)
	return err
}

// moveSubvolume renames a subvolume within the app-homes filesystem. Same-fs, so
// it is a metadata rename (fast, atomic), used to swap a rollback's staged home in.
func (m *Manager) moveSubvolume(src, dst string) error {
	_, err := m.runner.Run("mv", src, dst)
	return err
}

// snapshotSubvolume snapshots src to dst; readonly makes a stable, immutable
// snapshot (what we keep) versus a writable copy (what a rollback or fork starts
// from).
func (m *Manager) snapshotSubvolume(src, dst string, readonly bool) error {
	args := []string{"btrfs", "subvolume", "snapshot"}
	if readonly {
		args = append(args, "-r")
	}
	args = append(args, src, dst)
	_, err := m.runner.RunTimeout(btrfsTimeout, args...)
	return err
}

// setQuota limits an app's home subvolume to diskMB via its qgroup; 0 clears the
// limit. Quota must already be enabled on the filesystem (done once at setup).
// This is the hard limit -- a write past it fails with EDQUOT rather than the app
// being stopped later by a background sweep.
func (m *Manager) setQuota(home string, diskMB int) error {
	limit := "none"
	if diskMB > 0 {
		limit = strconv.Itoa(diskMB) + "M"
	}
	_, err := m.runner.RunTimeout(btrfsTimeout, "btrfs", "qgroup", "limit", limit, home)
	return err
}

// subvolumeUsageMB returns how much the subvolume at home references, in MB, from
// its qgroup (accurate and cheap, no directory walk). Returns 0 if it cannot read
// it, so reporting degrades rather than fails.
func (m *Manager) subvolumeUsageMB(home string) int {
	out, err := m.runner.RunTimeout(btrfsTimeout, "btrfs", "qgroup", "show", "-f", "--raw", home)
	if err != nil {
		return 0
	}
	return parseQgroupReferencedMB(out)
}

// parseQgroupReferencedMB reads the referenced bytes from `btrfs qgroup show -f
// --raw` output and returns whole megabytes. The table looks like:
//
//	Qgroupid         Referenced    Exclusive  Path
//	--------         ----------    ---------  ----
//	0/257            134217728     134217728  blog
//
// We take the Referenced column of the single data row.
func parseQgroupReferencedMB(out string) int {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.Contains(fields[0], "/") {
			continue // header, separator, or blank
		}
		bytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		return int(bytes / (1024 * 1024))
	}
	return 0
}

// snapshotID builds a sortable, unique id from a timestamp: seconds precision plus
// a short suffix so several snapshots in the same second do not collide.
func snapshotID(t time.Time, suffix string) string {
	return fmt.Sprintf("%s-%s", t.UTC().Format("20060102-150405"), suffix)
}
