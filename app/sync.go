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

// SyncState is the registry slice a node mirrors.
type SyncState struct {
	Apps      []*store.App      `json:"apps"`
	Snapshots []*store.Snapshot `json:"snapshots"`
}

// ControlSink is the node's reverse channel to control.
type ControlSink interface {
	// PowerChanged reports a poweroff/poweron the node's own verb performed.
	PowerChanged(name string, poweredOff bool)
	// UsageChanged reports a fresh disk usage measurement.
	UsageChanged(name string, usedMB int)
	// SnapshotsChanged carries the app's authoritative snapshot records after
	// any mutation; control replaces its rows with them.
	SnapshotsChanged(name string, snaps []*store.Snapshot)
}

// SetControlSink wires the node's reverse channel; nil (the default) means
// single-process, where the store writes land in the registry directly.
func (m *Manager) SetControlSink(sink ControlSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = sink
}

// Sync is the NodeAgent verb's node side: swap the mirror for the pushed
// state. The first sync also opens the gate for destructive startup work
// (ReconcileOrphans must never run against an unsynced, possibly empty
// mirror -- it would tear down every app on the machine).
func (m *Manager) Sync(state *SyncState) error {
	if err := m.store.ReplaceNodeMirror(state.Apps, state.Snapshots); err != nil {
		return err
	}
	m.syncedOnce.Do(func() { close(m.synced) })
	slog.Info("Registry mirror synced", "apps", len(state.Apps), "snapshots", len(state.Snapshots))
	return nil
}

// Synced closes once the first registry mirror arrived.
func (m *Manager) Synced() <-chan struct{} {
	return m.synced
}

// PushMirror sends the registry's current app/snapshot rows to the node;
// called on connect (rejoin) and after every registry mutation. A no-op in a
// single process, where the store IS the registry.
func (m *Manager) PushMirror() {
	if m.node == NodeAgent(m) {
		return
	}
	state, err := m.syncState()
	if err != nil {
		slog.Warn("Cannot build the mirror sync state", "error", err)
		return
	}
	if err := m.node.Sync(state); err != nil {
		slog.Warn("Cannot push the registry mirror to the node", "error", err)
	}
}

// syncState assembles the full mirror payload from the registry.
func (m *Manager) syncState() (*SyncState, error) {
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	state := &SyncState{Apps: apps, Snapshots: make([]*store.Snapshot, 0)}
	for _, a := range apps {
		snaps, err := m.store.Snapshots(a.Name)
		if err != nil {
			return nil, err
		}
		state.Snapshots = append(state.Snapshots, snaps...)
	}
	return state, nil
}

// The notify helpers guard the optional sink under the manager's lock.
func (m *Manager) notifyPower(name string, off bool) {
	if sink := m.controlSink(); sink != nil {
		sink.PowerChanged(name, off)
	}
}

func (m *Manager) notifyUsage(name string, usedMB int) {
	if sink := m.controlSink(); sink != nil {
		sink.UsageChanged(name, usedMB)
	}
}

// SnapshotsChanged implements the snapshot service's host callback: after any
// record mutation, ship the app's authoritative list to control.
func (m *Manager) SnapshotsChanged(name string) {
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

func (m *Manager) controlSink() ControlSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink
}
