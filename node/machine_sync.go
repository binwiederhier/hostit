package node

import (
	"log/slog"

	"heckel.io/hostit/nodeapi"
)

// SetControlSink wires the node's reverse channel; nil (the default) means
// single-process, where the store writes land in the registry directly.
func (m *Machine) SetControlSink(sink nodeapi.ControlSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = sink
}

// Sync is the nodeapi.NodeAgent verb's node side: swap the mirror for the pushed
// state. The first sync also opens the gate for destructive startup work
// (ReconcileOrphans must never run against an unsynced, possibly empty
// mirror -- it would tear down every app on the Machine).
func (m *Machine) Sync(state *nodeapi.SyncState) error {
	// Drop a payload older than the last one applied: control builds a mirror
	// by reading its registry and sends it afterwards, so two concurrent
	// mutations can arrive out of order, and the older one would delete a
	// just-created app from this mirror (see nodeapi.SyncState.Seq).
	m.syncMu.Lock()
	if state.Seq != 0 && state.Seq <= m.syncSeq {
		applied := m.syncSeq
		m.syncMu.Unlock()
		slog.Debug("Ignoring a stale registry mirror", "seq", state.Seq, "applied", applied)
		return nil
	}
	m.syncSeq = state.Seq
	m.syncMu.Unlock()

	if err := m.store.ReplaceNodeMirror(state.Apps, state.Snapshots); err != nil {
		return err
	}
	m.syncedOnce.Do(func() { close(m.synced) })
	slog.Info("Registry mirror synced", "apps", len(state.Apps), "snapshots", len(state.Snapshots))
	return nil
}

// ResetSyncSeq forgets the mirror sequence, called when a new control
// connection is established: the numbers come from control's process, so a
// restarted (or different) control starts counting again and its first push
// must not look stale.
func (m *Machine) ResetSyncSeq() {
	m.syncMu.Lock()
	m.syncSeq = 0
	m.syncMu.Unlock()
}

// Synced closes once the first registry mirror arrived.
func (m *Machine) Synced() <-chan struct{} {
	return m.synced
}

// The notify helpers guard the optional sink under the manager's lock.
func (m *Machine) notifyPower(name string, off bool) {
	if sink := m.controlSink(); sink != nil {
		sink.PowerChanged(name, off)
	}
}

func (m *Machine) notifyUsage(name string, usedMB int) {
	if sink := m.controlSink(); sink != nil {
		sink.UsageChanged(name, usedMB)
	}
}

// SnapshotsChanged implements the snapshot service's host callback: after any
// record mutation, ship the app's authoritative list to control.
func (m *Machine) SnapshotsChanged(name string) {
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

func (m *Machine) controlSink() nodeapi.ControlSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink
}
