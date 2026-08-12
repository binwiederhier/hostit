package app

import (
	"log/slog"
	"time"
)

// diskUsageInterval is how often app disk usage is re-measured for the dashboard.
// It is pure accounting (the qgroup enforces the quota), so a fixed 5 minutes is
// plenty and there is no reason to make it configurable.
const diskUsageInterval = 5 * time.Minute

// SetDiskLimit records the disk quota for an app; 0 means unlimited. It also sets
// the subvolume's qgroup limit, which hard-caps writes (EDQUOT).
func (m *Manager) SetDiskLimit(name string, diskMB int) {
	m.mu.Lock()
	m.diskMB[name] = diskMB
	m.mu.Unlock()
	if err := m.btrfs.SetQuota(m.appHome(name), diskMB); err != nil {
		slog.Warn("Cannot set btrfs quota", "app", name, "limit_mb", diskMB, "error", err)
	}
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

// measureDiskMB returns the app's disk usage in MB, read from the subvolume's
// qgroup (accurate and cheap, no directory walk).
func (m *Manager) measureDiskMB(name string) (int, error) {
	return m.btrfs.UsageMB(m.appHome(name)), nil
}
