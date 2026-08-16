package app

import (
	"heckel.io/hostit/store"
)

// ListSnapshots returns an app's snapshots, newest first.
func (m *Manager) ListSnapshots(name string) ([]*store.Snapshot, error) {
	return m.store.Snapshots(name)
}
