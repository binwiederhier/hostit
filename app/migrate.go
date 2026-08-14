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
	// settingStorageUnified records the hostit version that completed the
	// one-time unification migration (home folded into the rootfs, one
	// subvolume per app); any recorded value means it ran.
	settingStorageUnified = "storage-unified"
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

// MigrateUnifiedStorage is the one-time migration that folds each app's home
// INTO its rootfs, leaving ONE subvolume per app: the OS tree the container
// runs, with the files at home/app inside it. It runs after
// MigrateRootfsStorage (a pre-rootfs host runs both, in order), is resumable
// per app, and detects an already-unified app by marker (its subvolume has a
// /usr; homes never contained one). Old snapshots are home-shaped and
// incompatible with whole-app rollback, so they are dropped (owner's call).
// Only a 100% pass records the settings gate.
func (m *Manager) MigrateUnifiedStorage(version string) error {
	settings, err := m.store.Settings()
	if err != nil {
		return err
	}
	if settings[settingStorageUnified] != "" {
		return nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range apps {
		if err := m.migrateAppUnified(a); err != nil {
			slog.Warn("Unified-storage migration failed for app", "app", a.Name, "error", err)
			failed++
			continue
		}
		slog.Info("App unified onto one subvolume", "app", a.Name)
	}
	if failed > 0 {
		return fmt.Errorf("unified-storage migration incomplete: %d of %d apps failed; retrying at next start", failed, len(apps))
	}
	// The staging dir is empty once every app has moved; drop it (best effort).
	_ = os.Remove(filepath.Join(m.config.AppsDir, legacyRootfsDirName))
	slog.Info("Unified-storage migration complete", "apps", len(apps), "version", version)
	return m.store.SetSetting(settingStorageUnified, version)
}

// migrateAppUnified folds one app's home into its staged rootfs and makes that
// THE app subvolume. Resumable: every step either detects it already happened
// or is idempotent, so a run killed anywhere converges on the next start.
func (m *Manager) migrateAppUnified(a *store.App) error {
	// Already on one subvolume (moved by an earlier partial run, or created by
	// post-unification code before the gate was recorded): only the ensure-style
	// tail is needed, so a run killed between the move and the account/budget
	// updates still converges.
	if m.appUnified(a.ID) {
		return m.finishUnify(a)
	}
	subvol := m.appSubvolumeByID(a.ID)
	legacy := m.legacyRootfsPath(a.ID)
	if _, err := os.Stat(legacy); err != nil {
		return fmt.Errorf("no rootfs to unify %s into (did the rootfs migration succeed?): %w", a.Name, err)
	}
	ids, err := m.lookupIDs(a.Name)
	if err != nil {
		return err
	}
	// Power the app down for the move: its container runs the subvolumes being
	// rearranged. Stop, NOT disable, so a powered-off app stays disabled and an
	// enabled one stays enabled; the container is removed because it was created
	// against paths that stop existing below (its config hash changed anyway, so
	// the way back up recreates it against the unified subvolume).
	wasRunning := m.isActive(a.Name)
	_ = m.systemd.Stop(workspace.UnitName(a.ID))
	_ = m.container.RemoveForce(workspace.ContainerName(a.ID))
	// Fold the home into the staged rootfs. The trailing /. covers dotfiles
	// (.ssh, .hostit); -a preserves ownership, modes and xattrs; and
	// --reflink=always makes it an instant CoW copy (same btrfs pool), so this
	// costs metadata, not data. Skipped when the home subvolume is already gone
	// (killed mid-swap last time); the copy then already lives in the rootfs.
	if _, err := os.Stat(subvol); err == nil {
		target := filepath.Join(legacy, workspace.FilesDir)
		if err := os.MkdirAll(target, homeMode); err != nil {
			return err
		}
		if _, err := m.runner.Run("chown", fmt.Sprintf("%d:%d", ids.UID, ids.GID), target); err != nil {
			return fmt.Errorf("cannot chown the files dir of %s: %w", a.Name, err)
		}
		if _, err := m.runner.Run("cp", "-a", "--reflink=always", subvol+"/.", target+"/"); err != nil {
			return fmt.Errorf("cannot copy the home of %s into its rootfs: %w", a.Name, err)
		}
	}
	// Old snapshots are home-shaped: whole-app rollback cannot use them, so they
	// are dropped before the home they were taken from.
	if err := m.purgeSnapshots(a.Name); err != nil {
		return err
	}
	// Swap: the old home subvolume goes, and the staged rootfs (home now inside)
	// becomes THE app subvolume. The rename keeps its rootid, so any existing
	// qgroup membership survives; finishUnify re-ensures it regardless.
	if _, err := os.Stat(subvol); err == nil {
		if err := m.btrfs.DeleteSubvolume(subvol); err != nil {
			return fmt.Errorf("cannot delete the old home of %s: %w", a.Name, err)
		}
	}
	if err := m.btrfs.MoveSubvolume(legacy, subvol); err != nil {
		return fmt.Errorf("cannot move the rootfs of %s into place: %w", a.Name, err)
	}
	if err := m.finishUnify(a); err != nil {
		return err
	}
	// Bring a previously RUNNING app back up; a powered-off one stays off. Best
	// effort: the layout has moved either way, and Up can be retried any time.
	if wasRunning {
		if _, err := m.Up(a.Name); err != nil {
			slog.Warn("Cannot start app after unifying its storage", "app", a.Name, "error", err)
		}
	}
	return nil
}

// finishUnify is the ensure-style tail of one app's unification: point the Unix
// account home at the files dir inside the subvolume (scp/sftp/rsync land on
// the app's files) and (re)join the subvolume to the app's disk budget.
func (m *Manager) finishUnify(a *store.App) error {
	if err := m.user.SetHome(a.Name, m.appFilesByID(a.ID).Path()); err != nil {
		return err
	}
	return m.ensureBudget(a)
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
