package control

import (
	"log/slog"

	"heckel.io/hostit/nodeapi"
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

// AppPolicy resolves the per-app settings that do not live on the app row: the
// complete SSH key set (the app's own keys plus every standing profile key)
// and the owner's resource limits. It is an interface because those answers
// need the user tables, which the server layer owns; without one the Manager
// falls back to the app's own keys and its recorded limits.
type AppPolicy interface {
	Keys(a *store.App) []string
	Limits(a *store.App) (memoryMB, diskMB int)
}

// SetPolicy wires the resolver used to build desired state (see AppPolicy).
func (m *Manager) SetPolicy(p AppPolicy) {
	m.policy = p
}

// DesiredState is what control asserts for one node: every app that
// belongs there, with everything needed to build it from nothing. This is the
// registry projected outward -- the node holds no authority of its own, so a
// node that crashed, was rebuilt or missed a mutation converges by replaying
// this.
func (m *Manager) DesiredState(nodeID string) (*nodeapi.DesiredState, error) {
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	desired := &nodeapi.DesiredState{Apps: make([]*nodeapi.AppDesired, 0, len(apps))}
	for _, a := range apps {
		if nodeID != "" && hostOrLocal(a.Host) != nodeID {
			continue
		}
		keys, memoryMB, diskMB := m.appPolicy(a)
		desired.Apps = append(desired.Apps, &nodeapi.AppDesired{
			ProvisionSpec: nodeapi.ProvisionSpec{
				Host:    hostOrLocal(a.Host),
				ID:      a.ID,
				Name:    a.Name,
				Port:    a.Port,
				SSHKeys: keys,
				URL:     m.URL(a),
				DiskMB:  diskMB,
			},
			MemoryMB:   memoryMB,
			CPUMilli:   m.CPULimit(a.Name),
			PoweredOff: a.PoweredOff,
		})
	}
	return desired, nil
}

// appPolicy resolves one app's keys and limits through the wired policy, or
// falls back to what the registry and the recorded limits say.
func (m *Manager) appPolicy(a *store.App) (keys []string, memoryMB, diskMB int) {
	if m.policy != nil {
		k := m.policy.Keys(a)
		mem, disk := m.policy.Limits(a)
		return k, mem, disk
	}
	k, err := m.store.AppKeys(a.Name)
	if err != nil {
		slog.Warn("Cannot read an app's keys for its desired state", "app", a.Name, "error", err)
	}
	return k, m.MemoryLimit(a.Name), m.DiskLimit(a.Name)
}

// ReconcileNodes hands every connected node the desired state and lets each
// converge. A no-op in a single process, where control IS the machine.
func (m *Manager) ReconcileNodes(desired *nodeapi.DesiredState) {
	m.node.Reconcile(desired)
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
		// A copy without the owner: who owns an app is a tenant fact the node
		// never reads (it provisions accounts, it does not attribute them), so
		// it has no business on every machine in the cluster.
		mirrored := *a
		mirrored.OwnerID = ""
		state.Apps = append(state.Apps, &mirrored)
		snaps, err := m.store.Snapshots(a.Name)
		if err != nil {
			return nil, err
		}
		state.Snapshots = append(state.Snapshots, snaps...)
	}
	return state, nil
}
