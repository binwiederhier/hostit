package control

import (
	"log/slog"
	"time"

	"heckel.io/hostit/retention"
	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/store"
)

// ListSnapshots returns an app's snapshots, newest first.
func (m *Manager) ListSnapshots(name string) ([]*store.Snapshot, error) {
	return m.store.Snapshots(name)
}

// AutoSnapshotLoop drives the hourly automatic snapshots from the control
// plane: every tick it sweeps the registry and commands each app's node
// through the node agent. The node no longer snapshots on its own timer, so
// a snapshot can only happen while control is connected -- its record always
// reaches the registry (the snapshot-during-outage loss is gone by design).
func (m *Manager) AutoSnapshotLoop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting snapshot loop", "interval", interval)
	defer slog.Info("Stopping snapshot loop")
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		m.autoSnapshotSweep()
	}
}

// autoSnapshotSweep snapshots every app through the node agent and prunes
// each app's records afterwards. An unreachable node only skips its own
// apps: no snapshot happens there, so nothing can be lost.
func (m *Manager) autoSnapshotSweep() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps for the snapshot sweep", "error", err)
		return
	}
	for _, a := range apps {
		if _, err := m.node.TakeSnapshot(a.Name, snapshot.AutoSnapshotLabel, true); err != nil {
			slog.Warn("Automatic snapshot failed", "app", a.Name, "error", err)
			continue
		}
		m.PruneSnapshots(a.Name)
	}
}

// PruneSnapshots applies the retention policy to an app's records and
// commands the node to delete whatever falls outside it. Retention is the
// control plane's decision: the node deletes exactly what it is told to and
// never prunes on its own.
func (m *Manager) PruneSnapshots(name string) {
	snaps, err := m.store.Snapshots(name)
	if err != nil {
		slog.Warn("Cannot list snapshots for retention", "app", name, "error", err)
		return
	}
	rs := make([]retention.Snapshot, len(snaps))
	for i, s := range snaps {
		rs[i] = retention.Snapshot{ID: s.ID, App: s.AppName, Label: s.Label, CreatedAt: s.CreatedAt, Auto: s.Auto}
	}
	_, prune := retention.Apply(rs, retention.Default)
	for _, p := range prune {
		if err := m.node.DeleteSnapshot(name, p.ID); err != nil {
			slog.Warn("Cannot delete pruned snapshot", "app", name, "id", p.ID, "error", err)
		}
	}
}
