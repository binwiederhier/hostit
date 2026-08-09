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
		if err := m.setQuota(m.appHome(name), diskMB); err != nil {
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
	btrfs := m.btrfsEnabled()
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
		// On btrfs the qgroup enforces the limit at write time (EDQUOT), so this loop
		// is only reporting -- there is nothing to stop. The soft fallback below only
		// applies on filesystems without hard quotas.
		if btrfs {
			continue
		}
		if overQuota && !a.OverQuota {
			slog.Warn("App over disk quota, stopping it", "app", a.Name, "usage_mb", usage, "limit_mb", limit)
			if _, err := m.runner.Run("systemctl", "stop", m.unitName(a.Name)); err != nil {
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

// measureDiskMB returns the app's disk usage in MB. On btrfs it reads the
// subvolume's qgroup (accurate and cheap); otherwise it walks the home directory.
func (m *Manager) measureDiskMB(name string) (int, error) {
	if m.btrfsEnabled() {
		return m.subvolumeUsageMB(m.appHome(name)), nil
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
