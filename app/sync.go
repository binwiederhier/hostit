package app

import (
	"log/slog"

	"heckel.io/hostit/store"
)

// The split's registry flow: control OWNS the registry; a node keeps a MIRROR
// of the app and snapshot rows it hosts in its own SQLite. Control pushes the
// mirror (Sync) on connect and after every registry mutation, BEFORE any verb
// that reads rows on the node; the node reports the control-plane data it
// originates (usage measurements, poweroff transitions its verbs make,
// snapshot record changes from the auto-snapshot loop and retention) back
// through a ControlSink over the same duplex connection.

// SetControlSink wires the node's reverse channel; nil (the default) means
// single-process, where the store writes land in the registry directly.
func (m *machine) SetControlSink(sink ControlSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = sink
}

// Sync is the NodeAgent verb's node side: swap the mirror for the pushed
// state. The first sync also opens the gate for destructive startup work
// (ReconcileOrphans must never run against an unsynced, possibly empty
// mirror -- it would tear down every app on the machine).
func (m *machine) Sync(state *SyncState) error {
	if err := m.store.ReplaceNodeMirror(state.Apps, state.Snapshots); err != nil {
		return err
	}
	m.syncedOnce.Do(func() { close(m.synced) })
	slog.Info("Registry mirror synced", "apps", len(state.Apps), "snapshots", len(state.Snapshots))
	return nil
}

// Synced closes once the first registry mirror arrived.
func (m *machine) Synced() <-chan struct{} {
	return m.synced
}

// PushMirror sends the registry's current app/snapshot rows to the node;
// called on connect (rejoin) and after every registry mutation. A no-op in a
// single process, where the store IS the registry.
func (m *Manager) PushMirror() {
	if m.node == NodeAgent(m) {
		return
	}
	if m.registry == nil {
		// Single remote agent (tests): push everything.
		state, err := m.syncState("")
		if err != nil {
			slog.Warn("Cannot build the mirror sync state", "error", err)
			return
		}
		if err := m.node.Sync(state); err != nil {
			slog.Warn("Cannot push the registry mirror to the node", "error", err)
		}
		return
	}
	for _, id := range m.registry.IDs() {
		agent := m.registry.Agent(id)
		if agent == nil {
			continue
		}
		m.PushMirrorTo(id, agent)
	}
}

// PushMirrorTo sends one node its slice of the registry (its apps and their
// snapshots); the rejoin handshake calls it directly for the connecting node.
func (m *Manager) PushMirrorTo(nodeID string, agent NodeAgent) {
	state, err := m.syncState(nodeID)
	if err != nil {
		slog.Warn("Cannot build the mirror sync state", "node", nodeID, "error", err)
		return
	}
	if err := agent.Sync(state); err != nil {
		slog.Warn("Cannot push the registry mirror to the node", "node", nodeID, "error", err)
	}
}

// syncState assembles the mirror payload: the given node's apps (all apps
// when host is empty) and their snapshots.
func (m *Manager) syncState(host string) (*SyncState, error) {
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	state := &SyncState{Apps: make([]*store.App, 0, len(apps)), Snapshots: make([]*store.Snapshot, 0)}
	for _, a := range apps {
		if host != "" && hostOrLocal(a.Host) != host {
			continue
		}
		state.Apps = append(state.Apps, a)
		snaps, err := m.store.Snapshots(a.Name)
		if err != nil {
			return nil, err
		}
		state.Snapshots = append(state.Snapshots, snaps...)
	}
	return state, nil
}

// The notify helpers guard the optional sink under the manager's lock.
func (m *machine) notifyPower(name string, off bool) {
	if sink := m.controlSink(); sink != nil {
		sink.PowerChanged(name, off)
	}
}

func (m *machine) notifyUsage(name string, usedMB int) {
	if sink := m.controlSink(); sink != nil {
		sink.UsageChanged(name, usedMB)
	}
}

// SnapshotsChanged implements the snapshot service's host callback: after any
// record mutation, ship the app's authoritative list to control.
func (m *machine) SnapshotsChanged(name string) {
	sink := m.controlSink()
	if sink == nil {
		return
	}
	snaps, err := m.snapshots.ListSnapshots(name)
	if err != nil {
		slog.Warn("Cannot list snapshots for the control callback", "app", name, "error", err)
		return
	}
	sink.SnapshotsChanged(name, snaps)
}

func (m *machine) controlSink() ControlSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink
}
