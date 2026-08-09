package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"heckel.io/hostit/store"
)

const (
	// snapshotHookTimeout bounds a hostit.yml snapshot.pre/post command
	snapshotHookTimeout = 30 * time.Second
	// snapshotDirMode is the mode of the .snapshots/<app> directory
	snapshotDirMode = 0o700
	// rollbackStagedSuffix names the writable copy of a rollback target, built
	// beside the home before the home is touched; rollbackOldSuffix names the old
	// home moved aside during the swap. Both are cleaned up as the rollback proceeds.
	rollbackStagedSuffix = ".rollback-staged"
	rollbackOldSuffix    = ".rollback-old"
	// autoSnapshotLabel labels the hourly automatic snapshots, and
	// preDeploySnapshotLabel the one taken just before a deploy, so the owner sees
	// why each unattended snapshot exists instead of a blank row.
	autoSnapshotLabel      = "Automated snapshot"
	preDeploySnapshotLabel = "Automated snapshot before deploy"
)

// ErrSnapshotsUnavailable is returned when snapshots are asked for on a host whose
// apps filesystem is not btrfs.
var ErrSnapshotsUnavailable = errors.New("snapshots are not available on this host")

// SnapshotsEnabled reports whether snapshots and rollback are available here.
func (m *Manager) SnapshotsEnabled() bool { return m.btrfsEnabled() }

// TakeSnapshot snapshots an app's home into a read-only subvolume and records it.
// label is an optional note; auto marks automatic snapshots (which retention may
// prune). The app's snapshot.pre hook runs first and aborts the snapshot if it
// fails, so a torn state is never captured; snapshot.post runs after (best effort).
func (m *Manager) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	defer m.lockApp(name)()
	return m.takeSnapshot(name, label, auto)
}

// takeSnapshot is TakeSnapshot without the per-app lock, for callers that already
// hold it (up, rollback).
func (m *Manager) takeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	if !m.btrfsEnabled() {
		return nil, ErrSnapshotsUnavailable
	}
	if _, err := m.store.App(name); err != nil {
		return nil, err
	}
	conf, _ := m.loadConfig(name) // hooks are optional; a missing/invalid config just means none

	if conf != nil && conf.Snapshot.Pre != "" {
		res, err := m.Exec(name, conf.Snapshot.Pre, snapshotHookTimeout)
		if err != nil {
			return nil, fmt.Errorf("snapshot pre hook could not run: %w", err)
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("snapshot pre hook failed (exit %d); snapshot aborted", res.ExitCode)
		}
	}

	now := time.Now()
	id := snapshotID(now, snapshotKind(auto)+"-"+randID())
	if err := os.MkdirAll(m.snapshotsRoot(name), snapshotDirMode); err != nil {
		return nil, err
	}
	if err := m.snapshotSubvolume(m.appHome(name), m.snapshotPath(name, id), true); err != nil {
		return nil, fmt.Errorf("cannot snapshot the app home: %w", err)
	}
	snap := &store.Snapshot{ID: id, AppName: name, Label: label, CreatedAt: now, Auto: auto}
	if err := m.store.AddSnapshot(snap); err != nil {
		_ = m.deleteSubvolume(m.snapshotPath(name, id)) // do not leak an unrecorded subvolume
		return nil, err
	}

	if conf != nil && conf.Snapshot.Post != "" {
		if _, err := m.Exec(name, conf.Snapshot.Post, snapshotHookTimeout); err != nil {
			slog.Warn("Snapshot post hook failed", "app", name, "error", err)
		}
	}
	m.pruneSnapshots(name)
	return snap, nil
}

// ListSnapshots returns an app's snapshots, newest first.
func (m *Manager) ListSnapshots(name string) ([]*store.Snapshot, error) {
	return m.store.Snapshots(name)
}

// DeleteSnapshot removes a single snapshot -- its subvolume and its record -- when
// an owner or agent deletes one by hand. The record is dropped only after the
// subvolume is gone, so a failed delete never orphans the subvolume.
func (m *Manager) DeleteSnapshot(name, id string) error {
	if !m.btrfsEnabled() {
		return ErrSnapshotsUnavailable
	}
	defer m.lockApp(name)()
	snap, err := m.store.Snapshot(id)
	if err != nil {
		return err
	}
	if snap.AppName != name {
		return store.ErrSnapshotNotFound
	}
	if err := m.deleteSubvolume(m.snapshotPath(name, id)); err != nil {
		return fmt.Errorf("cannot delete the snapshot subvolume: %w", err)
	}
	return m.store.DeleteSnapshot(id)
}

// Rollback restores an app's home from a snapshot. The replacement is built and
// swapped in atomically so a failure never leaves the app without a home:
//
//  1. stage a writable copy of the target snapshot (before touching the home, and
//     before the safety snapshot -- whose retention prune could otherwise delete
//     the very target being restored);
//  2. take a safety snapshot of the current state (so the rollback is itself undoable);
//  3. stop the container, then swap: move the live home aside, move the staged copy
//     in, and only then drop the old home;
//  4. restore ownership and quota and bring the app back up.
//
// The per-app lock serializes this against concurrent deploys/snapshots on the app.
func (m *Manager) Rollback(name, id string) error {
	if !m.btrfsEnabled() {
		return ErrSnapshotsUnavailable
	}
	defer m.lockApp(name)()

	snap, err := m.store.Snapshot(id)
	if err != nil {
		return err
	}
	if snap.AppName != name {
		return store.ErrSnapshotNotFound
	}
	a, err := m.store.App(name)
	if err != nil {
		return err
	}
	defer m.stateChanged(name)

	home := m.appHome(name)
	staged := home + rollbackStagedSuffix
	oldHome := home + rollbackOldSuffix

	// Stage the restored home from the target first, so the live home stays intact
	// until the replacement is ready, and the content is safely captured before the
	// safety snapshot's retention prune (which could remove the target).
	_ = m.deleteSubvolume(staged) // clear any leftover from an aborted rollback
	if err := m.snapshotSubvolume(m.snapshotPath(name, id), staged, false); err != nil {
		return fmt.Errorf("cannot stage the snapshot for rollback: %w", err)
	}

	// The safety snapshot is itself automatic (retention prunes it in time) and
	// labelled so the owner can see what it captured.
	if _, err := m.takeSnapshot(name, "Before rolling back to snapshot "+id, true); err != nil {
		_ = m.deleteSubvolume(staged)
		return fmt.Errorf("cannot take a safety snapshot before rolling back: %w", err)
	}

	// Stop and remove the container so nothing holds the home subvolume.
	_, _ = m.runner.Run("systemctl", "disable", "--now", m.unitName(name))
	_, _ = m.runner.Run("systemctl", "reset-failed", m.unitName(name))
	_, _ = m.runner.Run("podman", "rm", "--force", m.containerName(name))

	// Swap the staged home in. Move the old home aside first so the home always
	// exists (old or new): if putting the new one in place fails, restore the old.
	_ = m.deleteSubvolume(oldHome)
	if err := m.moveSubvolume(home, oldHome); err != nil {
		_ = m.deleteSubvolume(staged)
		return fmt.Errorf("cannot move the current home aside: %w", err)
	}
	if err := m.moveSubvolume(staged, home); err != nil {
		_ = m.moveSubvolume(oldHome, home) // put the original home back
		return fmt.Errorf("cannot put the restored home in place: %w", err)
	}
	_ = m.deleteSubvolume(oldHome)

	uid := m.uidFor(a.Port)
	if _, err := m.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", uid, uid), home); err != nil {
		slog.Warn("Cannot restore home ownership after rollback", "app", name, "error", err)
	}
	if err := m.setQuota(home, m.diskLimit(name)); err != nil {
		slog.Warn("Cannot restore quota after rollback", "app", name, "error", err)
	}
	// No extra pre-deploy snapshot: we already took the safety snapshot above.
	_, err = m.up(name, false)
	return err
}

// pruneSnapshots deletes the snapshots that fall outside the retention policy,
// removing both the subvolume and the record.
func (m *Manager) pruneSnapshots(name string) {
	snaps, err := m.store.Snapshots(name)
	if err != nil {
		return
	}
	_, prune := applyRetention(toRetentionSnaps(snaps), defaultRetention)
	for _, p := range prune {
		if err := m.deleteSubvolume(m.snapshotPath(name, p.ID)); err != nil {
			slog.Warn("Cannot delete pruned snapshot subvolume", "app", name, "id", p.ID, "error", err)
			continue // keep the record so we retry, rather than orphan the subvolume
		}
		_ = m.store.DeleteSnapshot(p.ID)
	}
}

// deleteAppSubvolumes removes an app's home and all its snapshot subvolumes, used
// when an app is deleted. On a btrfs host `btrfs subvolume delete` cleans the whole
// subvolume; the caller then leaves an empty plain directory for userdel to remove.
func (m *Manager) deleteAppSubvolumes(name string) {
	snaps, _ := m.store.Snapshots(name)
	for _, s := range snaps {
		_ = m.deleteSubvolume(m.snapshotPath(name, s.ID))
	}
	_ = os.RemoveAll(m.snapshotsRoot(name))
	_ = m.deleteSubvolume(m.appHome(name))
}

// SnapshotLoop takes an automatic snapshot of every app on an interval (hourly),
// so an owner always has a recent point to roll back to.
func (m *Manager) SnapshotLoop(interval time.Duration, done <-chan struct{}) {
	if !m.btrfsEnabled() {
		return // nothing to snapshot on a non-btrfs host
	}
	slog.Info("Starting snapshot loop", "interval", interval)
	defer slog.Info("Stopping snapshot loop")
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		apps, err := m.store.Apps()
		if err != nil {
			continue
		}
		for _, a := range apps {
			if _, err := m.TakeSnapshot(a.Name, autoSnapshotLabel, true); err != nil {
				slog.Warn("Hourly snapshot failed", "app", a.Name, "error", err)
			}
		}
	}
}

func toRetentionSnaps(ss []*store.Snapshot) []Snapshot {
	out := make([]Snapshot, len(ss))
	for i, s := range ss {
		out[i] = Snapshot{ID: s.ID, App: s.AppName, Label: s.Label, CreatedAt: s.CreatedAt, Auto: s.Auto}
	}
	return out
}

func snapshotKind(auto bool) string {
	if auto {
		return "auto"
	}
	return "manual"
}

func randID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
