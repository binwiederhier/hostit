package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// BackfillAppIDs assigns a stable id to any app that predates app ids, so id is a
// complete identity before anything keys durable resources on it. Idempotent (a
// no-op once every app has an id) and best-effort (logged, never fatal).
func (m *Manager) BackfillAppIDs() {
	if err := m.store.BackfillAppIDs(); err != nil {
		slog.Warn("Cannot backfill app ids", "error", err)
		return
	}
	// FK columns copy from app.id, so the app ids must exist first.
	if err := m.store.BackfillFKAppIDs(); err != nil {
		slog.Warn("Cannot backfill per-app table app ids", "error", err)
	}
}

// MigrateToIDKeyedHomes is a one-off that moves every pre-id app's home (and its
// snapshots) from the old name-keyed path to its id-keyed path, so from then on a
// rename never has to move data. It is the ONLY time an existing app's container
// is recreated: the home's bind-mount source changes, which podman can only pick
// up on recreate. After this, the home, snapshots and container are all id-keyed,
// and a rename touches none of them.
//
// Idempotent (an app already at its id-keyed home is skipped) and best-effort (a
// per-app failure is logged and the app left as-is). Runs before serving so no
// request resolves a home that has not moved yet.
func (m *Manager) MigrateToIDKeyedHomes() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("home migration: cannot list apps", "error", err)
		return
	}
	for _, a := range apps {
		if a.ID == "" {
			continue // not backfilled yet; BackfillAppIDs runs first, so this is defensive
		}
		oldHome := filepath.Join(m.config.AppsDir, a.Name)
		newHome := m.appHomeByID(a.ID)
		if oldHome == newHome || !dirExists(oldHome) {
			continue // already migrated (or nothing to move)
		}
		started := time.Now()
		// Stop and remove the app's container before moving its home: it bind-mounts
		// the home, and podman can only pick up the new mount source on a recreate.
		// The container is still NAME-keyed here (this app predates id-keying), so
		// tear that one down; also try the id-keyed names in case a prior run got
		// partway. All best-effort.
		_ = m.systemd.DisableNow(unitTemplate + a.Name)
		_ = m.systemd.Stop(unitNameForID(a.ID))
		_ = m.container.RemoveForce(containerPrefix + a.Name)
		_ = m.container.RemoveForce(containerNameForID(a.ID))
		// An id-keyed container that started before this ran leaves podman's empty
		// stub at newHome (a bind-mount source it auto-creates). Now that the
		// container is gone, clear an EMPTY stub so the real home can take its place;
		// a non-empty newHome means a real migration already happened, so leave it.
		if dirExists(newHome) {
			if empty, err := isEmptyDir(newHome); err == nil && empty {
				_ = os.Remove(newHome)
			} else {
				slog.Warn("home migration: new home exists and is not an empty stub, leaving as-is", "app", a.Name, "old", oldHome, "new", newHome)
				continue
			}
		}
		if err := os.Rename(oldHome, newHome); err != nil {
			slog.Error("home migration: cannot move home, left as-is", "app", a.Name, "from", oldHome, "to", newHome, "error", err)
			continue
		}
		// Point the Unix user at the moved home (the files did not change owner).
		if err := m.ops.SetUserHome(a.Name, newHome); err != nil {
			slog.Error("home migration: home moved but usermod failed; fix manually", "app", a.Name, "home", newHome, "error", err)
		}
		// Move the snapshots directory alongside it if present.
		oldSnaps := filepath.Join(m.config.AppsDir, snapshotsDirName, a.Name)
		newSnaps := filepath.Join(m.config.AppsDir, snapshotsDirName, a.ID)
		if dirExists(oldSnaps) && !dirExists(newSnaps) {
			if err := os.Rename(oldSnaps, newSnaps); err != nil {
				slog.Warn("home migration: cannot move snapshots dir", "app", a.Name, "from", oldSnaps, "to", newSnaps, "error", err)
			}
		}
		// Re-apply the disk quota: applyStoredLimits ran before the home moved, so its
		// btrfs qgroup attempt hit the not-yet-existent id path. Now the home is here.
		m.SetDiskLimit(a.Name, m.diskLimit(a.Name))
		// Bring the app back up on its new home.
		if _, err := m.Ensure(a.Name); err != nil {
			slog.Error("home migration: moved but could not restart", "app", a.Name, "error", err)
		}
		slog.Info("Migrated app home to its id-keyed path", "app", a.Name, "id", a.ID, "took", time.Since(started).Round(time.Second))
	}
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// isEmptyDir reports whether a directory has no entries.
func isEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// MigrateToBlockUIDs is a one-off that moves any app still on the old split-uid
// scheme onto its contiguous uid block, so it too creates instantly (podman
// idmap-mounts the image) and stops holding a private chowned copy of it. New
// apps are born on their block, so this only ever touches pre-migration apps.
// Idempotent (an app already on its block is skipped) and best-effort (a per-app
// failure is logged, never fatal).
func (m *Manager) MigrateToBlockUIDs() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("uid migration: cannot list apps", "error", err)
		return
	}
	migrated := false
	for _, a := range apps {
		want := m.uidFor(a.Port)
		have, err := m.ops.LookupUID(a.Name)
		if err != nil {
			slog.Warn("uid migration: cannot read app uid, skipping", "app", a.Name, "error", err)
			continue
		}
		if have == want {
			continue // already on its block
		}
		started := time.Now()
		// Stop the app first: usermod refuses a uid change while the user has live
		// processes, and removing the container frees its old chowned image copy.
		_ = m.systemd.Stop(unitNameForID(a.ID))
		_ = m.container.RemoveForce(containerNameForID(a.ID))
		if err := m.ops.RemapUser(a.Name, m.appHomeByID(a.ID), want); err != nil {
			slog.Error("uid migration: cannot remap app, left as-is", "app", a.Name, "from", have, "to", want, "error", err)
			continue
		}
		if _, err := m.Ensure(a.Name); err != nil {
			slog.Error("uid migration: remapped but could not restart", "app", a.Name, "error", err)
			continue
		}
		migrated = true
		slog.Info("uid migration: moved app to its uid block", "app", a.Name, "from", have, "to", want, "took", time.Since(started).Round(time.Second))
	}
	// The nftables rules key off the uid, so rebuild them once the uids have moved
	if migrated {
		m.ReconcilePortRules()
	}
}

// RestartStaleAgents restarts every running app whose agent predates this build
// and returns the names it restarted.
//
// The agent is PID 1 in an app's container, exec'd from the hostit binary as it
// was at the time. The binary on disk is bind-mounted, so an upgrade replaces
// the file, but a running agent keeps the behaviour it started with -- and it
// is the agent that decides what the app's run command actually is. A static
// app once kept serving its old directory through an upgrade that way, with the
// app's whole home on the internet. Restarting costs each app a moment of
// downtime, so it only happens when the version actually changed.
func (m *Manager) RestartStaleAgents(version string) ([]string, error) {
	settings, err := m.store.Settings()
	if err != nil {
		return nil, err
	}
	if settings[settingAgentVersion] == version {
		return nil, nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	restarted := make([]string, 0, len(apps))
	for _, a := range apps {
		// Up, not just a restart: a new binary may also want the container built
		// differently (different mounts, different arguments), and only apply
		// notices that
		if _, err := m.Up(a.Name); err != nil {
			slog.Warn("Cannot bring app up after upgrade", "app", a.Name, "error", err)
			continue
		}
		restarted = append(restarted, a.Name)
	}
	return restarted, m.store.SetSetting(settingAgentVersion, version)
}
