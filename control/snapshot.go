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

// IngestNodeSnapshots recovers the snapshot records a node holds for the apps
// it hosts. Control calls this on rejoin BEFORE pushing the mirror back: a
// snapshot that completed as the connection dropped never delivered its
// SnapshotsChanged callback, and the push would otherwise overwrite the node's
// rows with control's older list -- losing the record while its subvolume
// stays (invisible to retention, still charged to the app's budget).
//
// Records for apps this node does not host are ignored, the same scoping the
// reverse-channel callbacks enforce.
func (m *Manager) IngestNodeSnapshots(nodeID string, agent NodeAgent) {
	reported, err := agent.Snapshots()
	if err != nil {
		slog.Warn("Cannot read a node's snapshot records on rejoin", "node", nodeID, "error", err)
		return
	}
	byApp := make(map[string][]*store.Snapshot)
	for _, snap := range reported {
		host, err := m.store.AppHost(snap.AppName)
		if err != nil {
			continue // the app is gone from the registry; the node's reconcile drops it
		}
		if hostOrLocal(host) != nodeID {
			slog.Warn("Ignoring snapshot records for an app the node does not host", "node", nodeID, "app", snap.AppName)
			continue
		}
		byApp[snap.AppName] = append(byApp[snap.AppName], snap)
	}
	for name, snaps := range byApp {
		if err := m.store.ReplaceAppSnapshots(name, snaps); err != nil {
			slog.Warn("Cannot ingest a node's snapshot records", "node", nodeID, "app", name, "error", err)
		}
	}
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
