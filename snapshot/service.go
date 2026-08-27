// Package snapshot orchestrates whole-app snapshots, rollback and retention on
// btrfs. A snapshot captures the app's one subvolume -- its files at home/app
// AND the installed software around them -- so a rollback restores both
// together. It composes the node-local services (btrfs, systemd, container) and
// the store directly, and calls back into its Host (the control.Manager) for the
// app-lifecycle operations a snapshot or rollback needs: taking the per-app lock,
// bringing the app up after a rollback, running snapshot hooks, resolving the
// id-keyed paths, names and uid of an app, and joining new subvolumes to the
// app's disk budget.
package snapshot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/systemd"
)

const (
	// snapshotHookTimeout bounds a hostit.yml snapshot.pre/post command
	snapshotHookTimeout = 30 * time.Second
	// snapshotDirMode is the mode of the .snapshots/<app> directory
	snapshotDirMode = 0o700
	// rollbackStagedSuffix names the writable copy of a rollback target, built
	// beside the app subvolume before it is touched; rollbackOldSuffix names the
	// old subvolume moved aside during the swap. Both are cleaned up as the
	// rollback proceeds.
	rollbackStagedSuffix = ".rollback-staged"
	rollbackOldSuffix    = ".rollback-old"
	// AutoSnapshotLabel labels the hourly automatic snapshots, and
	// preDeploySnapshotLabel the one taken just before a deploy, so the owner sees
	// why each unattended snapshot exists instead of a blank row.
	AutoSnapshotLabel      = "Automated snapshot"
	preDeploySnapshotLabel = "Automated snapshot before deploy"
)

// Host is the set of app-orchestration callbacks the Service needs from its owner
// (control.Manager). Everything node-local (btrfs, systemd, container, the store) the
// Service holds directly; these are the app-lifecycle operations and the id-keyed
// path/name/uid lookups that stay in the node package, so the path layout and
// the deploy machinery have a single home.
type Host interface {
	// LockApp acquires the per-app lifecycle lock and returns its unlock func, so a
	// snapshot or rollback never races a deploy on the same app's home.
	LockApp(name string) func()
	// Up brings the app up after a rollback. It must NOT take the per-app lock (the
	// caller holds it) nor a pre-deploy snapshot (the rollback already took a safety
	// one) -- the control.Manager's unlocked up path.
	Up(name string) error
	// StateChanged drops the app's cached state after its home or process moved.
	StateChanged(name string)
	// SnapshotsChanged reports that the app's snapshot RECORDS changed
	// (created, deleted, pruned); the split-mode host ships the authoritative
	// list to control, which owns the metadata the UI and retention read.
	SnapshotsChanged(name string)
	// SnapshotHooks returns the app's snapshot.pre/post commands from its
	// hostit.yml; empty strings when there is no (valid) config or no such hook.
	SnapshotHooks(name string) (pre, post string)
	// RunHook runs a snapshot hook command inside the app's container and returns
	// its exit code. A non-nil error means the command could not be run at all.
	RunHook(name, command string, timeout time.Duration) (exitCode int, err error)
	// AppSubvolume is the app's one subvolume: the container's whole OS tree
	// with the files at home/app inside, which is what snapshots capture and
	// rollback swaps.
	AppSubvolume(name string) string
	// SnapshotsRoot is the app's snapshots directory, <apps>/.snapshots/<id>.
	SnapshotsRoot(name string) string
	// SnapshotPath is one snapshot's subvolume path.
	SnapshotPath(name, id string) string
	// UnitName and ContainerName are the app's systemd unit and container names.
	UnitName(name string) string
	ContainerName(name string) string
	// BudgetGroup returns the app's disk budget qgroup id ("1/<uid>"), or ""
	// when it cannot be resolved -- the snapshot is then created unbudgeted
	// rather than failing. Every
	// subvolume this service creates must join, or extents the app subvolume
	// shares with a snapshot become reachable outside the group and stop counting
	// as its exclusive bytes -- app data would silently leak out of the app's cap.
	BudgetGroup(name string) string
}

// Service performs whole-app snapshots, rollback and retention pruning.
type Service struct {
	btrfs     btrfs.Interface
	systemd   systemd.Interface
	container podman.Interface
	store     *store.Store
	host      Host
}

// New builds a snapshot Service from the node-local services, the store and the
// host callbacks.
func New(bt btrfs.Interface, sd systemd.Interface, ct podman.Interface, st *store.Store, host Host) *Service {
	return &Service{btrfs: bt, systemd: sd, container: ct, store: st, host: host}
}

// TakeSnapshot snapshots the app's whole subvolume (files AND installed
// software) into a read-only subvolume and records it. label is an optional
// note; auto marks automatic snapshots (which retention may prune). The app's
// snapshot.pre hook runs first and aborts the snapshot if it fails, so a torn
// state is never captured; snapshot.post runs after (best effort).
func (s *Service) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	defer s.host.LockApp(name)()
	return s.takeSnapshot(name, label, auto)
}

// takeSnapshot is TakeSnapshot without the per-app lock, for callers that already
// hold it (PreDeploySnapshot, Rollback).
func (s *Service) takeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	if _, err := s.store.App(name); err != nil {
		return nil, err
	}
	pre, post := s.host.SnapshotHooks(name) // hooks are optional; a missing/invalid config just means none

	if pre != "" {
		code, err := s.host.RunHook(name, pre, snapshotHookTimeout)
		if err != nil {
			return nil, fmt.Errorf("snapshot pre hook could not run: %w", err)
		}
		if code != 0 {
			return nil, fmt.Errorf("snapshot pre hook failed (exit %d); snapshot aborted", code)
		}
	}

	now := time.Now()
	id := snapshotID(now, snapshotKind(auto)+"-"+randID())
	if err := os.MkdirAll(s.host.SnapshotsRoot(name), snapshotDirMode); err != nil {
		return nil, err
	}
	if err := s.btrfs.Snapshot(s.host.AppSubvolume(name), s.host.SnapshotPath(name, id), true, s.host.BudgetGroup(name)); err != nil {
		return nil, fmt.Errorf("cannot snapshot the app subvolume: %w", err)
	}
	snap := &store.Snapshot{ID: id, AppName: name, Label: label, CreatedAt: now, Auto: auto}
	if err := s.store.AddSnapshot(snap); err != nil {
		_ = s.btrfs.DeleteSubvolume(s.host.SnapshotPath(name, id)) // do not leak an unrecorded subvolume
		return nil, err
	}

	if post != "" {
		if _, err := s.host.RunHook(name, post, snapshotHookTimeout); err != nil {
			slog.Warn("Snapshot post hook failed", "app", name, "error", err)
		}
	}
	s.host.SnapshotsChanged(name)
	return snap, nil
}

// PreDeploySnapshot takes the automatic safety snapshot a deploy makes before
// applying a new config, so a bad deploy is undoable. The caller (the app's
// unlocked up path) already holds the per-app lock, and a failure only warns: a
// snapshot failure must not block the deploy.
func (s *Service) PreDeploySnapshot(name string) {
	if _, err := s.takeSnapshot(name, preDeploySnapshotLabel, true); err != nil {
		slog.Warn("Pre-deploy snapshot failed", "app", name, "error", err)
	}
}

// ListSnapshots returns an app's snapshots, newest first.
func (s *Service) ListSnapshots(name string) ([]*store.Snapshot, error) {
	return s.store.Snapshots(name)
}

// DeleteSnapshot removes a single snapshot -- its subvolume and its record -- when
// an owner or agent deletes one by hand. The record is dropped only after the
// subvolume is gone, so a failed delete never orphans the subvolume.
func (s *Service) DeleteSnapshot(name, id string) error {
	defer s.host.LockApp(name)()
	snap, err := s.store.Snapshot(id)
	if err != nil {
		return err
	}
	if snap.AppName != name {
		return store.ErrSnapshotNotFound
	}
	if err := s.btrfs.DeleteSubvolume(s.host.SnapshotPath(name, id)); err != nil {
		return fmt.Errorf("cannot delete the snapshot subvolume: %w", err)
	}
	if err := s.store.DeleteSnapshot(id); err != nil {
		return err
	}
	s.host.SnapshotsChanged(name)
	return nil
}

// Rollback restores an app from a snapshot: the snapshot is the whole app
// subvolume, so the app's files AND its installed software come back together.
// The replacement is built and swapped in atomically so a failure never leaves
// the app without a subvolume:
//
//  1. stage a writable copy of the target snapshot (before touching the live
//     subvolume, so the content is captured before anything else happens);
//  2. take a safety snapshot of the current state (so the rollback is itself undoable);
//  3. power the container down (disable the unit and remove the container -- the
//     subvolume being swapped IS the container's rootfs, so nothing may run it
//     during the swap), then swap: move the live subvolume aside, move the
//     staged copy in, and only then drop the old one;
//  4. bring the app back up (no ownership to restore: trees are root-owned
//     and idmap-mounted, and snapshots carry that with them).
//
// The per-app lock serializes this against concurrent deploys/snapshots on the app.
func (s *Service) Rollback(name, id string) error {
	defer s.host.LockApp(name)()

	snap, err := s.store.Snapshot(id)
	if err != nil {
		return err
	}
	if snap.AppName != name {
		return store.ErrSnapshotNotFound
	}
	// The app row is only checked for existence: everything below keys on the
	// name/id paths the host callbacks resolve.
	if _, err := s.store.App(name); err != nil {
		return err
	}
	defer s.host.StateChanged(name)

	subvol := s.host.AppSubvolume(name)
	staged := subvol + rollbackStagedSuffix
	oldSubvol := subvol + rollbackOldSuffix

	// Stage the restored subvolume from the target first, so the live one stays
	// intact until the replacement is ready.
	_ = s.btrfs.DeleteSubvolume(staged) // clear any leftover from an aborted rollback
	if err := s.btrfs.Snapshot(s.host.SnapshotPath(name, id), staged, false, s.host.BudgetGroup(name)); err != nil {
		return fmt.Errorf("cannot stage the snapshot for rollback: %w", err)
	}
	// The staged copy was created inside the disk budget (-i above) and becomes
	// the app's subvolume after the swap; qgroup membership survives the rename.

	// The safety snapshot is itself automatic (control's retention prunes it in
	// time) and labelled so the owner can see what it captured.
	if _, err := s.takeSnapshot(name, "Before rolling back to snapshot "+id, true); err != nil {
		_ = s.btrfs.DeleteSubvolume(staged)
		return fmt.Errorf("cannot take a safety snapshot before rolling back: %w", err)
	}

	// Power the container down so nothing runs the subvolume being swapped: it is
	// the container's rootfs, so the unit is stopped AND the container removed
	// (apply recreates it against the restored subvolume on the way back up).
	_ = s.systemd.DisableNow(s.host.UnitName(name))
	_ = s.systemd.ResetFailed(s.host.UnitName(name))
	_ = s.container.RemoveForce(s.host.ContainerName(name))

	// Swap the staged subvolume in. Move the old one aside first so the app always
	// has a subvolume (old or new): if putting the new one in place fails, restore
	// the old.
	_ = s.btrfs.DeleteSubvolume(oldSubvol)
	if err := s.btrfs.MoveSubvolume(subvol, oldSubvol); err != nil {
		_ = s.btrfs.DeleteSubvolume(staged)
		return fmt.Errorf("cannot move the current app subvolume aside: %w", err)
	}
	if err := s.btrfs.MoveSubvolume(staged, subvol); err != nil {
		_ = s.btrfs.MoveSubvolume(oldSubvol, subvol) // put the original back
		return fmt.Errorf("cannot put the restored app subvolume in place: %w", err)
	}
	_ = s.btrfs.DeleteSubvolume(oldSubvol)

	// No ownership to restore: the whole tree is root-owned and idmap-mounted,
	// and snapshots carry that with them.
	// No quota to restore: the cap lives on the app's budget qgroup, and the new
	// subvolume was assigned to it when it was staged above.
	// No extra pre-deploy snapshot: we already took the safety snapshot above.
	return s.host.Up(name)
}

// DeleteAppSubvolumes removes an app's subvolume and all its snapshot
// subvolumes, used when an app is deleted. On a btrfs host `btrfs subvolume
// delete` cleans the whole subvolume; the caller then removes whatever stub
// userdel leaves behind.
func (s *Service) DeleteAppSubvolumes(name string) {
	snaps, _ := s.store.Snapshots(name)
	for _, snap := range snaps {
		_ = s.btrfs.DeleteSubvolume(s.host.SnapshotPath(name, snap.ID))
	}
	_ = os.RemoveAll(s.host.SnapshotsRoot(name))
	_ = s.btrfs.DeleteSubvolume(s.host.AppSubvolume(name))
}

// snapshotID builds a sortable, unique id from a timestamp: seconds precision plus
// a short suffix so several snapshots in the same second do not collide.
func snapshotID(t time.Time, suffix string) string {
	return fmt.Sprintf("%s-%s", t.UTC().Format("20060102-150405"), suffix)
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
