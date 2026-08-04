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

// SetDiskLimit records the disk quota for an app; 0 means unlimited
func (m *Manager) SetDiskLimit(name string, diskMB int) {
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

// CheckQuotas measures every app's disk usage, records it, and stops apps that
// exceed their quota (a soft quota: ext4 without project quotas cannot hard-cap,
// so this is periodic accounting plus enforcement, not a kernel limit). Apps
// that dropped back below their quota are simply un-flagged; restarting them is
// left to the owner via "hostit up".
func (m *Manager) CheckQuotas() error {
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
		limit := m.diskLimit(a.Name)
		overQuota := limit > 0 && usage > limit
		if err := m.store.UpdateAppUsage(a.Name, usage, overQuota); err != nil {
			slog.Warn("Cannot record disk usage", "app", a.Name, "error", err)
			continue
		}
		if overQuota && !a.OverQuota {
			slog.Warn("App over disk quota, stopping it", "app", a.Name, "usage_mb", usage, "limit_mb", limit)
			if _, err := m.runner.Run("systemctl", "stop", unitName(a.Name)); err != nil {
				slog.Warn("Cannot stop over-quota app", "app", a.Name, "error", err)
			}
		} else if !overQuota && a.OverQuota {
			slog.Info("App back within disk quota", "app", a.Name, "usage_mb", usage)
		}
	}
	return nil
}

// QuotaLoop periodically runs CheckQuotas until the context-free stop channel closes
func (m *Manager) QuotaLoop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting disk quota loop", "interval", interval)
	defer slog.Info("Stopping disk quota loop")
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		if err := m.CheckQuotas(); err != nil {
			slog.Warn("Disk quota check failed", "error", err)
		}
	}
}

// measureDiskMB returns the app's disk usage in MB: its home directory, which
// since the move to root-managed containers holds only the app's own files (the
// image store is shared and lives with the daemon)
func (m *Manager) measureDiskMB(name string) (int, error) {
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
