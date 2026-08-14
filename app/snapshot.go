package app

import (
	"fmt"
	"time"

	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/store"
)

// TakeSnapshot snapshots an app's whole subvolume (files AND installed
// software) into a read-only subvolume and records it.
func (m *Manager) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	return m.snapshots.TakeSnapshot(name, label, auto)
}

// ListSnapshots returns an app's snapshots, newest first.
func (m *Manager) ListSnapshots(name string) ([]*store.Snapshot, error) {
	return m.snapshots.ListSnapshots(name)
}

// DeleteSnapshot removes a single snapshot (its subvolume and its record).
func (m *Manager) DeleteSnapshot(name, id string) error {
	return m.snapshots.DeleteSnapshot(name, id)
}

// Rollback restores an app from a snapshot: its files AND its installed
// software come back together, since the snapshot is the whole app subvolume.
func (m *Manager) Rollback(name, id string) error {
	return m.snapshots.Rollback(name, id)
}

// SnapshotLoop takes an automatic snapshot of every app on an interval (hourly).
func (m *Manager) SnapshotLoop(interval time.Duration, done <-chan struct{}) {
	m.snapshots.SnapshotLoop(interval, done)
}

// snapshotHost adapts the Manager to snapshot.Host: it exposes the per-app lock,
// the unlocked deploy path, the id-keyed path/name/uid lookups and the disk
// budget assignment the snapshot Service calls back for. It is a thin binding,
// so the Manager's public API stays free of these callbacks (and of a second
// method named Exec).
type snapshotHost struct{ m *Manager }

var _ snapshot.Host = snapshotHost{}

func (h snapshotHost) LockApp(name string) func() { return h.m.lockApp(name) }

func (h snapshotHost) Up(name string) error {
	_, err := h.m.up(name, false)
	return err
}

func (h snapshotHost) StateChanged(name string) { h.m.stateChanged(name) }

func (h snapshotHost) SnapshotHooks(name string) (pre, post string) {
	conf, _ := h.m.loadConfig(name) // hooks are optional; a missing/invalid config just means none
	if conf == nil {
		return "", ""
	}
	return conf.Snapshot.Pre, conf.Snapshot.Post
}

func (h snapshotHost) RunHook(name, command string, timeout time.Duration) (int, error) {
	res, err := h.m.Exec(name, command, timeout)
	if err != nil {
		return 0, err
	}
	return res.ExitCode, nil
}

func (h snapshotHost) AppSubvolume(name string) string     { return h.m.appSubvolume(name) }
func (h snapshotHost) SnapshotsRoot(name string) string    { return h.m.snapshotsRoot(name) }
func (h snapshotHost) SnapshotPath(name, id string) string { return h.m.snapshotPath(name, id) }
func (h snapshotHost) UnitName(name string) string         { return h.m.unitName(name) }
func (h snapshotHost) ContainerName(name string) string    { return h.m.containerName(name) }
func (h snapshotHost) UIDForPort(port int) int             { return h.m.uidFor(port) }

func (h snapshotHost) AssignBudget(name, subvolPath string) error {
	return h.m.assignBudget(name, subvolPath)
}

func (h snapshotHost) Chown(path string, uid int) error {
	_, err := h.m.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", uid, uid), path)
	return err
}
