package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"heckel.io/hostit/store"
)

const (
	// snapshotHookTimeout bounds a hostit.yml snapshot.pre/post command
	snapshotHookTimeout = 30 * time.Second
	// snapshotDirMode is the mode of the .snapshots/<app> directory
	snapshotDirMode = 0o700
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

// Rollback restores an app's home from a snapshot. It first takes a safety snapshot
// of the current state (so a rollback is itself undoable), then stops the app,
// replaces the home subvolume with a writable copy of the snapshot, restores its
// ownership and quota, and brings the app back up.
func (m *Manager) Rollback(name, id string) error {
	if !m.btrfsEnabled() {
		return ErrSnapshotsUnavailable
	}
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

	// The safety snapshot is itself automatic (retention prunes it in time) and
	// labelled so the owner can see what it captured.
	if _, err := m.TakeSnapshot(name, "Before rolling back to snapshot "+id, true); err != nil {
		return fmt.Errorf("cannot take a safety snapshot before rolling back: %w", err)
	}

	// Stop and remove the container so nothing holds the home subvolume.
	_, _ = m.runner.Run("systemctl", "disable", "--now", unitName(name))
	_, _ = m.runner.Run("systemctl", "reset-failed", unitName(name))
	_, _ = m.runner.Run("podman", "rm", "--force", containerName(name))

	home := m.appHome(name)
	if err := m.deleteSubvolume(home); err != nil {
		return fmt.Errorf("cannot clear the current home: %w", err)
	}
	if err := m.snapshotSubvolume(m.snapshotPath(name, id), home, false); err != nil {
		return fmt.Errorf("cannot restore the snapshot: %w", err)
	}
	uid := m.uidFor(a.Port)
	if _, err := m.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", uid, uid), home); err != nil {
		slog.Warn("Cannot restore home ownership after rollback", "app", name, "error", err)
	}
	if err := m.setQuota(home, m.diskLimit(name)); err != nil {
		slog.Warn("Cannot restore quota after rollback", "app", name, "error", err)
	}
	_, err = m.Up(name)
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
			if _, err := m.TakeSnapshot(a.Name, "", true); err != nil {
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

// Snapshot is one point-in-time copy of an app's home (a read-only btrfs subvolume).
// Auto records how it was taken -- automatically (before a deploy or assistant turn,
// and hourly) or manually (a labelled save the owner/agent asked for). Retention
// applies to all of them, so no snapshot lives forever.
type Snapshot struct {
	ID        string    // Unique, sortable; the subvolume's directory name
	App       string    // The app it belongs to
	Label     string    // Optional human note; empty for automatic snapshots
	CreatedAt time.Time // When it was taken
	Auto      bool      // Taken automatically (retention applies) vs. on purpose
}

// RetentionPolicy is a restic-style grandfather-father-son policy: keep the most
// recent Last snapshots, plus the newest snapshot in each of the last Daily days,
// Weekly ISO weeks and Monthly months. The union is kept; the rest are pruned.
type RetentionPolicy struct {
	Last    int
	Daily   int
	Weekly  int
	Monthly int
}

// defaultRetention keeps a dense recent history and thins it out with age.
var defaultRetention = RetentionPolicy{Last: 50, Daily: 7, Weekly: 4, Monthly: 3}

// applyRetention partitions snapshots into those to keep and those to prune under
// the policy. Every snapshot -- manual and automatic alike -- is subject to it, so
// none lives forever. Input need not be sorted; the result is order-independent.
func applyRetention(snaps []Snapshot, p RetentionPolicy) (keep, prune []Snapshot) {
	all := append([]Snapshot(nil), snaps...)
	// Newest first, deterministic on ties so pruning is stable.
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	kept := make(map[string]bool, len(all))
	// keep-last: the newest N outright.
	for i := 0; i < p.Last && i < len(all); i++ {
		kept[all[i].ID] = true
	}
	// keep-daily/weekly/monthly: the newest snapshot of each of the last N buckets.
	markBuckets(all, p.Daily, dayBucket, kept)
	markBuckets(all, p.Weekly, weekBucket, kept)
	markBuckets(all, p.Monthly, monthBucket, kept)

	for _, s := range all {
		if kept[s.ID] {
			keep = append(keep, s)
		} else {
			prune = append(prune, s)
		}
	}
	return keep, prune
}

// markBuckets walks snapshots newest-first and keeps the first (newest) snapshot of
// each of the first n distinct buckets, where bucket() names a snapshot's day, week
// or month.
func markBuckets(sortedNewestFirst []Snapshot, n int, bucket func(time.Time) string, kept map[string]bool) {
	if n <= 0 {
		return
	}
	prev := ""
	seen := 0
	for _, s := range sortedNewestFirst {
		b := bucket(s.CreatedAt)
		if b == prev {
			continue // an older snapshot in a bucket we already handled
		}
		prev = b
		if seen >= n {
			return // enough buckets kept for this tier
		}
		kept[s.ID] = true
		seen++
	}
}

// Bucket keys are computed in UTC so retention is deterministic and independent of
// the server's local timezone.
func dayBucket(t time.Time) string { return t.UTC().Format("2006-01-02") }

func weekBucket(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

func monthBucket(t time.Time) string { return t.UTC().Format("2006-01") }
