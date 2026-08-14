package app

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

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
	// settingStorageIdmap records the hostit version that completed the one-time
	// move to idmapped rootfs mounts (trees root-owned, ownership mapped by the
	// runtime); any recorded value means it ran.
	settingStorageIdmap = "storage-idmap"
	// settingPowerOffBackfill records that the poweroff store flag was seeded
	// from systemd unit state once; after that, the flag is authoritative.
	settingPowerOffBackfill = "poweroff-flag-backfill"
	// legacyRootfsDirName is where the pre-unification layout kept the per-app
	// rootfs subvolumes (beside the then-separate homes). Only the migrations
	// still know this path: MigrateRootfsStorage stages rootfses here on a
	// pre-rootfs host, and the unification migration consumes and removes them.
	legacyRootfsDirName = ".rootfs"
	// legacyRootfsDirMode keeps .rootfs root-only, as the workspace service did
	// when it owned the path.
	legacyRootfsDirMode = 0o700
	// migrateCopyTimeout bounds the reflink fold of a home into its rootfs. The
	// file count is tenant-controlled; measured at ~74ms per real app, so the
	// bound only cuts off pathological trees.
	migrateCopyTimeout = 15 * time.Minute
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
	// An already-unified app (created by newer code, or moved by the unification
	// migration) needs no legacy rootfs; there is nothing left to fold.
	if m.appUnified(a) {
		return nil
	}
	if err := m.ensureLegacyRootfs(a); err != nil {
		return err
	}
	return m.purgeSnapshots(a.Name)
}

// ensureLegacyRootfs snapshots the app's pinned base into the legacy .rootfs
// location. It lives here because only a pre-rootfs host mid-migration ever
// needs a rootfs at this path; the unification migration moves it onto the app
// subvolume moments later, and the idmap migration then leaves the whole tree
// root-owned -- so no ownership is baked in here at all.
func (m *Manager) ensureLegacyRootfs(a *store.App) error {
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
	return nil
}

// MigrateUnifiedStorage is the one-time migration that folds each app's home
// INTO its rootfs, leaving ONE subvolume per app: the OS tree the container
// runs, with the files at home/app inside it. It runs after
// MigrateRootfsStorage (a pre-rootfs host runs both, in order), and is
// resumable per app: the staged .rootfs/<id> subvolume (root-owned, so a tenant
// cannot fake it away) marks an app that still needs folding, and the passwd
// home (root-maintained) marks one that is already unified. Old snapshots are
// home-shaped and incompatible with whole-app rollback, so they are dropped
// (owner's call). Only a 100% pass records the settings gate.
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
	// The staged rootfs is the root-controlled "still needs folding" marker: the
	// swap below is what consumes it, so while it exists the fold has not
	// completed, and once it is gone only the ensure-style tail can be left
	// (moved by an earlier partial run, or created by post-unification code
	// before the gate was recorded). Nothing INSIDE the app subvolume can decide
	// this: in the old layout the subvolume root was the tenant's home.
	subvol := m.appSubvolumeByID(a.ID)
	legacy := m.legacyRootfsPath(a.ID)
	if _, err := os.Stat(legacy); err != nil {
		if m.appUnified(a) {
			return m.finishUnify(a)
		}
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
		if _, err := m.runner.RunTimeout(migrateCopyTimeout, "cp", "-a", "--reflink=always", subvol+"/.", target+"/"); err != nil {
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
	// Point the passwd home inside BEFORE the move: the move consumes the staged
	// rootfs (the "needs folding" marker), so the moment it lands, the "already
	// unified" marker -- the root-maintained passwd home -- must already read
	// true, or a crash between the two would strand the app in neither state.
	if err := m.user.SetHome(a.Name, m.appFilesByID(a.ID).Path()); err != nil {
		return err
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

// appUnified reports whether an app is on the unified layout: its passwd home
// points at the files dir INSIDE the subvolume. The passwd entry is
// root-maintained state; a marker inside the subvolume (a /usr, say) would be
// tenant-forgeable, because in the pre-unification layout the subvolume root
// was the tenant's own home.
func (m *Manager) appUnified(a *store.App) bool {
	home, err := m.user.Home(a.Name)
	return err == nil && home == m.appFilesByID(a.ID).Path()
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

// MigrateIdmapStorage is the one-time migration to idmapped rootfs mounts: the
// container maps the root-owned subvolume through its uid mapping, so nothing
// is ever chowned into an app tree again. Existing trees have the app's uid
// block baked in (the pre-idmap chown -R); every inode in that block shifts
// back to its container-relative id (base+u -> u), the /etc/mtab symlink podman
// must not create through the idmapped view is retrofitted, and pre-idmap
// snapshots -- whose baked-in ownership rollback can no longer use -- are
// dropped (owner's call, like every storage migration before it). Resumable:
// the shift and the symlink are idempotent, and only a 100% pass records the
// settings gate.
func (m *Manager) MigrateIdmapStorage(version string) error {
	settings, err := m.store.Settings()
	if err != nil {
		return err
	}
	if settings[settingStorageIdmap] != "" {
		return nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range apps {
		if err := m.migrateAppIdmap(a); err != nil {
			slog.Warn("Idmap migration failed for app", "app", a.Name, "error", err)
			failed++
			continue
		}
		slog.Info("App moved to idmapped rootfs", "app", a.Name)
	}
	if failed > 0 {
		return fmt.Errorf("idmap migration incomplete: %d of %d apps failed; retrying at next start", failed, len(apps))
	}
	slog.Info("Idmap migration complete", "apps", len(apps), "version", version)
	return m.store.SetSetting(settingStorageIdmap, version)
}

// migrateAppIdmap moves one app onto the idmapped layout. The container is
// stopped for the shift (its config hash changes anyway: the rootfs gained
// :idmap) and a previously running app comes back up; a powered-off one stays
// off, like in every other migration.
func (m *Manager) migrateAppIdmap(a *store.App) error {
	subvol := m.appSubvolumeByID(a.ID)
	if _, err := os.Stat(subvol); err != nil {
		return fmt.Errorf("no subvolume to migrate for %s: %w", a.Name, err)
	}
	wasRunning := m.isActive(a.Name)
	_ = m.systemd.Stop(workspace.UnitName(a.ID))
	_ = m.container.RemoveForce(workspace.ContainerName(a.ID))
	// The block base derives from the port, exactly like the container's uid map.
	if err := shiftTreeToRoot(subvol, m.uidFor(a.Port), workspace.UIDBlockSize, os.Lchown); err != nil {
		return fmt.Errorf("cannot shift ownership of %s: %w", a.Name, err)
	}
	if err := workspace.WriteMtab(subvol); err != nil {
		return fmt.Errorf("cannot write mtab for %s: %w", a.Name, err)
	}
	if err := m.purgeSnapshots(a.Name); err != nil {
		return err
	}
	if wasRunning {
		if _, err := m.Up(a.Name); err != nil {
			slog.Warn("Cannot start app after idmap migration", "app", a.Name, "error", err)
		}
	}
	return nil
}

// shiftID maps one owner id out of an app's contiguous block back to its
// container-relative value (base+u -> u); ids outside the block -- real root,
// nobody, another app's block -- are unchanged.
func shiftID(id, base, count int) (int, bool) {
	if id >= base && id < base+count {
		return id - base, true
	}
	return id, false
}

// shiftTreeToRoot walks a subvolume and shifts every inode owned inside the
// app's id block back to its container-relative owner. The chown is injected so
// tests can observe the mapping without running as root; production passes
// os.Lchown, which never follows the (tenant-plantable) final symlink.
func shiftTreeToRoot(root string, base, count int, lchown func(path string, uid, gid int) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info() // Lstat semantics: symlinks are not followed
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		uid, uidHit := shiftID(int(st.Uid), base, count)
		gid, gidHit := shiftID(int(st.Gid), base, count)
		if !uidHit && !gidHit {
			return nil
		}
		return lchown(path, uid, gid)
	})
}

// BackfillPowerOffFlags seeds the poweroff store flag from systemd once: before
// the flag existed, only a deliberate poweroff ever disabled a unit, so a
// disabled unit at backfill time IS recorded intent. One-time and gated -- after
// this, the flag is authoritative and unit state is never consulted again (a
// never-enabled fresh unit also reads "disabled", which is exactly the
// ambiguity the flag exists to remove).
func (m *Manager) BackfillPowerOffFlags() error {
	settings, err := m.store.Settings()
	if err != nil {
		return err
	}
	if settings[settingPowerOffBackfill] != "" {
		return nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if m.systemd.IsEnabled(workspace.UnitName(a.ID)) {
			continue
		}
		if err := m.store.SetAppPoweredOff(a.Name, true); err != nil {
			return err
		}
		slog.Info("Recorded existing poweroff", "app", a.Name)
	}
	return m.store.SetSetting(settingPowerOffBackfill, "done")
}
