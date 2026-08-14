package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

const (
	// settingStorageMigrated records the hostit version that completed the
	// one-time rootfs storage migration; any recorded value means it ran.
	settingStorageMigrated = "storage-rootfs-migrated"
	// legacyRootfsDirName is where the pre-unification layout kept the per-app
	// rootfs subvolumes (beside the then-separate homes). Only the migrations
	// still know this path: MigrateRootfsStorage stages rootfses here on a
	// pre-rootfs host, and the unification migration consumes and removes them.
	legacyRootfsDirName = ".rootfs"
	// legacyRootfsDirMode keeps .rootfs root-only, as the workspace service did
	// when it owned the path.
	legacyRootfsDirMode = 0o700
)

// MigrateRootfsStorage is the one-time migration from image-backed containers to
// per-app rootfs subvolumes. For every app it backfills a missing image tag,
// creates a rootfs at the legacy .rootfs/<id> location and drops all existing
// snapshots (owner's call: keeps post-migration budgets predictable). It is step
// one of two on a pre-rootfs host: MigrateUnifiedStorage runs right after and
// folds each home into its rootfs (and sets up the disk budgets).
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

// migrateAppStorage moves one app onto rootfs storage: build its legacy rootfs
// (the unification migration then folds the home into it) and drop its old
// snapshots. Budgets are left to the unification migration that runs right after.
func (m *Manager) migrateAppStorage(a *store.App) error {
	ids, err := m.lookupIDs(a.Name)
	if err != nil {
		return err
	}
	// An already-unified app (created by newer code, or moved by the unification
	// migration) needs no legacy rootfs; there is nothing left to fold.
	if m.appUnified(a.ID) {
		return nil
	}
	if err := m.ensureLegacyRootfs(a, ids); err != nil {
		return err
	}
	return m.purgeSnapshots(a.Name)
}

// ensureLegacyRootfs snapshots the app's pinned base into the legacy .rootfs
// location, chowned to the app's id block -- what the workspace service did
// before the unified layout. It lives here because only a pre-rootfs host
// mid-migration ever needs a rootfs at this path; the unification migration
// moves it onto the app subvolume moments later.
func (m *Manager) ensureLegacyRootfs(a *store.App, ids workspace.IDs) error {
	rootfs := m.legacyRootfsPath(a.ID)
	if _, err := os.Stat(rootfs); err == nil {
		return nil
	}
	tag := a.ImageTag // pinned by PinImageTags before any app migrates
	if err := m.workspace.EnsureBase(tag); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rootfs), legacyRootfsDirMode); err != nil {
		return err
	}
	if err := m.btrfs.Snapshot(m.workspace.BasePath(tag), rootfs, false); err != nil {
		return fmt.Errorf("cannot snapshot rootfs for %s: %w", a.Name, err)
	}
	if _, err := m.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", ids.UID, ids.GID), rootfs); err != nil {
		return fmt.Errorf("cannot chown rootfs %s: %w", rootfs, err)
	}
	return nil
}

// legacyRootfsPath is an app's rootfs subvolume in the pre-unification layout.
func (m *Manager) legacyRootfsPath(id string) string {
	return filepath.Join(m.config.AppsDir, legacyRootfsDirName, id)
}

// appUnified reports whether an app is on the unified layout: its subvolume is
// a full OS tree. Homes never contained /usr, so that is the marker.
func (m *Manager) appUnified(id string) bool {
	_, err := os.Stat(filepath.Join(m.appSubvolumeByID(id), "usr"))
	return err == nil
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
