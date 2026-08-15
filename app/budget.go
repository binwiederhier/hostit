package app

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/btrfs"
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

// ensureBudget sets up an app's disk budget: one qgroup keyed on the app's uid
// (stable across renames, unique per app) that the app subvolume joins (its
// snapshots join as they are taken), hard-capped on EXCLUSIVE bytes -- what the
// app itself pins, with extents shared with the base charged to nobody.
// Ensure-style: every step is idempotent, so it is safe to re-run at startup or
// after a partial failure.
func (m *Manager) ensureBudget(a *store.App) error {
	ids, err := m.lookupIDs(a.Name)
	if err != nil {
		return err
	}
	pool := m.config.AppsDir
	group := budgetGroup(ids.UID)
	// Create errors on an existing group; that is the common re-run case, and a
	// real failure surfaces in the assign right after, so it is not checked.
	_ = m.btrfs.QgroupCreate(pool, group)
	if err := m.assignToGroup(m.appSubvolumeByID(a.ID), group); err != nil {
		return fmt.Errorf("cannot assign the app subvolume to the disk budget of %s: %w", a.Name, err)
	}
	if err := m.btrfs.QgroupLimitExclusive(pool, group, effectiveDiskCapMB(m.DiskLimit(a.Name))); err != nil {
		return fmt.Errorf("cannot cap disk budget of %s: %w", a.Name, err)
	}
	return nil
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

// destroyBudgetGently is destroyBudget for the background teardown: the full
// ladder's filesystem sync forces a transaction commit that stalls every
// concurrent btrfs operation on the pool, so this polls a plain destroy until
// the deleted member subvolumes have committed on their own. A group that
// outlasts the patience is left for the startup reconcile's sweep.
func (m *Manager) destroyBudgetGently(uid int) {
	deadline := time.Now().Add(budgetDestroyWait)
	for {
		err := m.btrfs.QgroupTryDestroy(m.config.AppsDir, budgetGroup(uid))
		if err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			slog.Warn("Budget qgroup still busy after teardown; the startup reconcile sweeps it", "uid", uid, "error", err)
			return
		}
		time.Sleep(budgetDestroyPoll)
	}
}

// budgetGroup is the app's qgroup id, keyed on its unix uid: stable across
// renames, unique per app (a fork gets its own uid block).
func budgetGroup(uid int) string {
	return btrfs.BudgetGroupPrefix + strconv.Itoa(uid)
}

// effectiveDiskCapMB maps the stored limit to the enforced one: an unset limit
// falls back to the default cap instead of meaning unlimited.
func effectiveDiskCapMB(diskMB int) int {
	if diskMB <= 0 {
		return defaultDiskCapMB
	}
	return diskMB
}

// SweepStaleQgroups destroys leftover qgroups: the 0/<rootid> group of a
// deleted subvolume and the 1/<uid> budget of a deleted app. Deletes destroy
// their qgroups gently (single attempt, never a filesystem sync, so creates
// stay fast), and a lost race leaves the group behind forever. Enough stale
// groups make every quota rescan slow -- and snapshot creation waits on
// rescans, so app creates eventually blow their deadline. This sweep is the
// backstop that keeps the pool lean.
func (m *Manager) SweepStaleQgroups() {
	pool := m.config.AppsDir
	groups, err := m.btrfs.ListQgroups(pool)
	if err != nil {
		return // No quotas on the pool (or no btrfs): nothing to sweep
	}
	subvols, err := m.btrfs.SubvolumeIDs(pool)
	if err != nil {
		slog.Warn("Cannot list subvolumes for the qgroup sweep", "error", err)
		return
	}
	live := make(map[string]bool, len(subvols))
	for _, id := range subvols {
		live[id] = true
	}
	live["5"] = true // The filesystem root: not listed by "subvolume list", never stale
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps for the qgroup sweep", "error", err)
		return
	}
	// A budget group is only stale if EVERY app's uid resolved; a failed lookup
	// must not get a live app's budget destroyed.
	budgets := make(map[string]bool, len(apps))
	budgetsComplete := true
	for _, a := range apps {
		ids, err := m.lookupIDs(a.Name)
		if err != nil {
			budgetsComplete = false
			continue
		}
		budgets[budgetGroup(ids.UID)] = true
	}
	destroyed := 0
	for _, g := range groups {
		stale := false
		if id, ok := strings.CutPrefix(g, "0/"); ok {
			stale = !live[id]
		} else if strings.HasPrefix(g, btrfs.BudgetGroupPrefix) {
			stale = budgetsComplete && !budgets[g]
		}
		if stale && m.btrfs.QgroupTryDestroy(pool, g) == nil {
			destroyed++
		}
	}
	if destroyed > 0 {
		slog.Info("Destroyed stale qgroups", "count", destroyed)
	}
}

// QgroupSweepLoop sweeps at start and then every interval, until done closes.
func (m *Manager) QgroupSweepLoop(interval time.Duration, done <-chan struct{}) {
	m.SweepStaleQgroups()
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		m.SweepStaleQgroups()
	}
}
