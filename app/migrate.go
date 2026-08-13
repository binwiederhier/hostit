package app

import (
	"fmt"
	"log/slog"
	"os"

	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

const (
	// settingStorageMigrated records the hostit version that completed the
	// one-time rootfs storage migration; any recorded value means it ran.
	settingStorageMigrated = "storage-rootfs-migrated"
)

// MigrateRootfsStorage is the one-time migration from image-backed containers to
// per-app rootfs subvolumes with a combined disk budget. For every app it
// backfills a missing image tag, creates the rootfs, drops all existing
// snapshots (owner's call: keeps post-migration budgets predictable) and sets up
// the budget qgroup; the containers themselves are then recreated by the normal
// config-hash change (the create args changed shape).
//
// Every step is ensure-style, so a run killed halfway resumes safely: only a
// fully successful pass records the settings gate, and later starts skip on it.
func (m *Manager) MigrateRootfsStorage(version string) error {
	settings, err := m.store.Settings()
	if err != nil {
		return err
	}
	if settings[settingStorageMigrated] != "" {
		return nil
	}
	// Backfill: apps from before image pinning ran the current image, so that is
	// the tag their rootfs is built from -- pinned explicitly now.
	if err := m.store.PinImageTags(workspace.ImageTag()); err != nil {
		return err
	}
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range apps {
		if err := m.migrateAppStorage(a); err != nil {
			slog.Warn("Storage migration failed for app", "app", a.Name, "error", err)
			failed++
			continue
		}
		slog.Info("App migrated to rootfs storage", "app", a.Name)
	}
	// An incomplete pass must not record the gate, so the next start retries the
	// failed apps (the succeeded ones are no-ops by then).
	if failed > 0 {
		return fmt.Errorf("storage migration incomplete: %d of %d apps failed; retrying at next start", failed, len(apps))
	}
	slog.Info("Storage migration complete", "apps", len(apps), "version", version)
	return m.store.SetSetting(settingStorageMigrated, version)
}

// migrateAppStorage moves one app onto rootfs storage: rootfs first (so both
// subvolumes exist), then the snapshot purge, then the budget around what is left.
func (m *Manager) migrateAppStorage(a *store.App) error {
	ids, err := m.lookupIDs(a.Name)
	if err != nil {
		return err
	}
	if err := m.workspace.EnsureRootfs(a, ids); err != nil {
		return err
	}
	if err := m.purgeSnapshots(a.Name); err != nil {
		return err
	}
	return m.ensureBudget(a)
}

// purgeSnapshots deletes every snapshot of an app, subvolume and store row. A
// subvolume already gone on disk only has its row dropped (the resume case); a
// subvolume that cannot be deleted keeps its row, so nothing is orphaned.
func (m *Manager) purgeSnapshots(name string) error {
	snaps, err := m.store.Snapshots(name)
	if err != nil {
		return err
	}
	for _, snap := range snaps {
		path := m.snapshotPath(name, snap.ID)
		if err := m.btrfs.DeleteSubvolume(path); err != nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return fmt.Errorf("cannot delete snapshot %s: %w", snap.ID, err)
			}
			// Already gone on disk; just drop the row below.
		}
		if err := m.store.DeleteSnapshot(snap.ID); err != nil {
			return err
		}
	}
	return nil
}
