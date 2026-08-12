package app

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"
)

const (
	// bytesPerMB converts measured bytes to the MB unit used everywhere else
	bytesPerMB = 1024 * 1024
)

// SetDiskLimit records the disk quota for an app; 0 means unlimited. On btrfs it
// also sets the subvolume's qgroup limit, which hard-caps writes (EDQUOT) rather
// than the soft measure-and-stop fallback used on other filesystems.
func (m *Manager) SetDiskLimit(name string, diskMB int) {
	m.mu.Lock()
	m.diskMB[name] = diskMB
	m.mu.Unlock()
	if m.btrfsEnabled() {
		if err := m.btrfs.SetQuota(m.appHome(name), diskMB); err != nil {
			slog.Warn("Cannot set btrfs quota", "app", name, "limit_mb", diskMB, "error", err)
		}
	}
}

// diskLimit returns the recorded disk quota of an app
func (m *Manager) diskLimit(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.diskMB[name]
}

// RefreshDiskUsage measures every app's disk usage and records it for the
// dashboard. Enforcement is left to the filesystem: on btrfs the qgroup hard-caps
// writes (EDQUOT) at SetDiskLimit time, so there is nothing here to stop -- this is
// pure accounting. (On a non-btrfs host there is no hard cap; disk use is reported
// but not enforced.)
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
func (m *Manager) DiskUsageLoop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting disk usage loop", "interval", interval)
	defer slog.Info("Stopping disk usage loop")
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		if err := m.RefreshDiskUsage(); err != nil {
			slog.Warn("Disk usage refresh failed", "error", err)
		}
	}
}

// measureDiskMB returns the app's disk usage in MB. On btrfs it reads the
// subvolume's qgroup (accurate and cheap); otherwise it walks the home directory.
func (m *Manager) measureDiskMB(name string) (int, error) {
	if m.btrfsEnabled() {
		return m.btrfs.UsageMB(m.appHome(name)), nil
	}
	var total int64
	err := filepath.WalkDir(m.appHome(name), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Unreadable entries (races, permissions) must not abort the walk
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(total / bytesPerMB), nil
}
