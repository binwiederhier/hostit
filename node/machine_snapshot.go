package node

import (
	"path/filepath"
	"time"

	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/store"
)

const (
	// snapshotsDirName holds read-only snapshots under the apps mount, one directory
	// per app. It sits beside the app subvolumes, not inside them: a snapshot must
	// not be part of the tree it captures (and the container must not see it).
	snapshotsDirName = ".snapshots"
)

// TakeSnapshot snapshots an app's whole subvolume (files AND installed
// software) into a read-only subvolume and records it.
func (m *Machine) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	return m.snapshots.TakeSnapshot(name, label, auto)
}

// DeleteSnapshot removes a single snapshot (its subvolume and its record).
func (m *Machine) DeleteSnapshot(name, id string) error {
	return m.snapshots.DeleteSnapshot(name, id)
}

// Rollback restores an app from a snapshot: its files AND its installed
// software come back together, since the snapshot is the whole app subvolume.
func (m *Machine) Rollback(name, id string) error {
	return m.snapshots.Rollback(name, id)
}

// snapshotHost adapts the Manager to snapshot.Host: it exposes the per-app lock,
// the unlocked deploy path, the id-keyed path/name/uid lookups and the disk
// budget assignment the snapshot Service calls back for. It is a thin binding,
// so the Manager's public API stays free of these callbacks (and of a second
// method named Exec).
type snapshotHost struct{ m *Machine }

var _ snapshot.Host = snapshotHost{}

func (h snapshotHost) LockApp(name string) func() { return h.m.LockApp(name) }

func (h snapshotHost) Up(name string) error {
	_, err := h.m.up(name, false)
	return err
}

func (h snapshotHost) StateChanged(name string) { h.m.stateChanged(name) }

func (h snapshotHost) SnapshotsChanged(name string) { h.m.SnapshotsChanged(name) }

func (h snapshotHost) SnapshotHooks(name string) (pre, post string) {
	conf, _ := h.m.LoadAppConfig(name) // hooks are optional; a missing/invalid config just means none
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

func (h snapshotHost) AppSubvolume(name string) string     { return h.m.AppSubvolume(name) }
func (h snapshotHost) SnapshotsRoot(name string) string    { return h.m.snapshotsRoot(name) }
func (h snapshotHost) SnapshotPath(name, id string) string { return h.m.SnapshotPath(name, id) }
func (h snapshotHost) UnitName(name string) string         { return h.m.UnitName(name) }
func (h snapshotHost) ContainerName(name string) string    { return h.m.ContainerName(name) }

func (h snapshotHost) BudgetGroup(name string) string {
	ids, err := h.m.LookupIDs(name)
	if err != nil {
		return "" // unbudgeted beats failing the snapshot
	}
	return budgetGroup(ids.UID)
}

// snapshotsRoot is where an app's snapshots live: <apps>/.snapshots/<id>/. Keyed
// on the app's id (like the app subvolume) so a rename does not move them.
func (m *Machine) snapshotsRoot(app string) string {
	return filepath.Join(m.config.AppsDir, snapshotsDirName, m.AppID(app))
}

// snapshotPath is one snapshot's subvolume path.
func (m *Machine) SnapshotPath(app, id string) string {
	return filepath.Join(m.snapshotsRoot(app), id)
}
