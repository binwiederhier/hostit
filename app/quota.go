package app

import (
	"log/slog"
	"time"
)

// diskUsageInterval is how often app disk usage is re-measured for the dashboard.
// It is pure accounting (the qgroup enforces the quota), so a fixed 5 minutes is
// plenty and there is no reason to make it configurable.
const diskUsageInterval = 5 * time.Minute

// SetDiskLimit records the disk quota for an app and caps its budget qgroup
// (home + rootfs + snapshots combined) on exclusive bytes, which hard-caps
// writes (EDQUOT). 0 falls back to the default cap; nothing is unlimited.
func (m *Manager) SetDiskLimit(name string, diskMB int) {
	m.recordDiskLimit(name, diskMB)
	ids, err := m.lookupIDs(name)
	if err != nil {
		slog.Warn("Cannot resolve uid to set disk budget", "app", name, "error", err)
		return
	}
	if err := m.btrfs.QgroupLimitExclusive(m.config.AppsDir, budgetGroup(ids.UID), effectiveDiskCapMB(diskMB)); err != nil {
		slog.Warn("Cannot set disk budget limit", "app", name, "limit_mb", diskMB, "error", err)
	}
}

// recordDiskLimit caches the stored limit without touching the qgroup, for the
// create path where the budget group does not exist yet (ensureBudget applies it).
func (m *Manager) recordDiskLimit(name string, diskMB int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diskMB[name] = diskMB
}

// diskLimit returns the recorded disk quota of an app
func (m *Manager) diskLimit(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.diskMB[name]
}

// RefreshDiskUsage measures every app's disk usage and records it for the
// dashboard. Enforcement is left to the filesystem: the qgroup hard-caps writes
// (EDQUOT) at SetDiskLimit time, so there is nothing here to stop -- this is pure
// accounting.
func (m *Manager) RefreshDiskUsage() error {
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		usage, err := m.measureDiskMB(a.Name)
		if err != nil {
			slog.Warn("Cannot measure disk usage", "app", a.Name, "error", err)
			continue
		}
		if err := m.store.UpdateAppUsage(a.Name, usage); err != nil {
			slog.Warn("Cannot record disk usage", "app", a.Name, "error", err)
		}
	}
	return nil
}

// DiskUsageLoop periodically refreshes recorded disk usage until the stop channel closes
func (m *Manager) DiskUsageLoop(done <-chan struct{}) {
	slog.Info("Starting disk usage loop", "interval", diskUsageInterval)
	defer slog.Info("Stopping disk usage loop")
	for {
		select {
		case <-time.After(diskUsageInterval):
		case <-done:
			return
		}
		if err := m.RefreshDiskUsage(); err != nil {
			slog.Warn("Disk usage refresh failed", "error", err)
		}
	}
}

// measureDiskMB returns the app's disk usage in MB: its budget group's exclusive
// bytes, i.e. the bytes the app itself pins (what deleting it would free), with
// the shared base rootfs charged to nobody. Cheap qgroup read, no directory walk.
func (m *Manager) measureDiskMB(name string) (int, error) {
	ids, err := m.lookupIDs(name)
	if err != nil {
		return 0, err
	}
	return m.btrfs.ExclusiveUsageMB(m.config.AppsDir, budgetGroup(ids.UID)), nil
}
