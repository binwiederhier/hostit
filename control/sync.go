package control

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
	// The read and the stamp are ONE step: stamping afterwards let two
	// builders interleave so that a payload read earlier got the higher
	// number, and the node -- which keeps only the highest -- then held the
	// older content for good. The lock spans the registry read only, never the
	// send, so a wedged node cannot block anyone else's push.
	m.mirrorMu.Lock()
	apps, err := m.store.Apps()
	if err != nil {
		m.mirrorMu.Unlock()
		return nil, err
	}
	seq := m.mirrorSeq.Add(1)
	m.mirrorMu.Unlock()
	state := &SyncState{Seq: seq, Apps: make([]*store.App, 0, len(apps)), Snapshots: make([]*store.Snapshot, 0)}
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
