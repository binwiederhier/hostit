package app

import (
	"fmt"
	"log/slog"

	"heckel.io/hostit/store"
)

const (
	// defaultDiskCapMB is the disk budget for apps with no explicit limit
	// (disk_mb 0, which used to mean unlimited). Nothing is ever unlimited
	// anymore: an uncapped app dd'ing into its rootfs once filled the whole host
	// (stage, 2026-08-12), taking the daemon's own SQLite down with it.
	defaultDiskCapMB = 2048
)

// EnableDiskBudgets turns on btrfs quota accounting for the apps pool, the
// mechanism behind every per-app disk budget; idempotent, called once at
// startup. Failure only warns: apps still run, just uncapped, which the next
// start retries.
func (m *Manager) EnableDiskBudgets() {
	if err := m.btrfs.QuotaEnable(m.config.AppsDir); err != nil {
		slog.Warn("Cannot enable btrfs quota accounting; disk budgets will not be enforced", "error", err)
	}
}

// ensureBudget sets up an app's combined disk budget: one qgroup keyed on the
// app's uid (stable across renames, unique per app) that its home and rootfs
// subvolumes join, hard-capped on EXCLUSIVE bytes -- what the app itself pins,
// with extents shared with the base charged to nobody. Ensure-style: every step
// is idempotent, so it is safe to re-run at startup or after a partial failure.
func (m *Manager) ensureBudget(a *store.App) error {
	ids, err := m.lookupIDs(a.Name)
	if err != nil {
		return err
	}
	pool := m.config.AppsDir
	group := budgetGroup(ids.UID)
	// Create errors on an existing group; that is the common re-run case, and a
	// real failure surfaces in the assigns right after, so it is not checked.
	_ = m.btrfs.QgroupCreate(pool, group)
	if err := m.assignToGroup(m.appHome(a.Name), group); err != nil {
		return fmt.Errorf("cannot assign home to disk budget of %s: %w", a.Name, err)
	}
	if err := m.assignToGroup(m.workspace.RootfsPath(a.ID), group); err != nil {
		return fmt.Errorf("cannot assign rootfs to disk budget of %s: %w", a.Name, err)
	}
	if err := m.btrfs.QgroupLimitExclusive(pool, group, effectiveDiskCapMB(m.diskLimit(a.Name))); err != nil {
		return fmt.Errorf("cannot cap disk budget of %s: %w", a.Name, err)
	}
	return nil
}

// assignBudget joins one subvolume (a snapshot, a staged rollback copy) to the
// app's budget group; the snapshot service calls this for every subvolume it
// creates, through the snapshot.Host callback.
func (m *Manager) assignBudget(name, subvolPath string) error {
	ids, err := m.lookupIDs(name)
	if err != nil {
		return err
	}
	return m.assignToGroup(subvolPath, budgetGroup(ids.UID))
}

// assignToGroup resolves a subvolume's own qgroup (0/<rootid>) and makes it a
// member of the app's budget group.
func (m *Manager) assignToGroup(subvolPath, group string) error {
	rootID, err := m.btrfs.RootID(subvolPath)
	if err != nil {
		return err
	}
	return m.btrfs.QgroupAssign(m.config.AppsDir, "0/"+rootID, group)
}

// destroyBudget removes an app's budget group on delete; best effort, since its
// member subvolumes are already gone and a stale empty group is only clutter.
func (m *Manager) destroyBudget(uid int) {
	if err := m.btrfs.QgroupDestroy(m.config.AppsDir, budgetGroup(uid)); err != nil {
		slog.Warn("Cannot destroy disk budget qgroup", "uid", uid, "error", err)
	}
}

// budgetGroup is the app's qgroup id, keyed on its unix uid: stable across
// renames, unique per app (a fork gets its own uid block).
func budgetGroup(uid int) string {
	return fmt.Sprintf("1/%d", uid)
}

// effectiveDiskCapMB maps the stored limit to the enforced one: an unset limit
// falls back to the default cap instead of meaning unlimited.
func effectiveDiskCapMB(diskMB int) int {
	if diskMB <= 0 {
		return defaultDiskCapMB
	}
	return diskMB
}
