package app

import (
	"path/filepath"
)

const (
	// snapshotsDirName holds read-only snapshots under the apps mount, one directory
	// per app. It sits beside the app subvolumes, not inside them: a snapshot must
	// not be part of the tree it captures (and the container must not see it).
	snapshotsDirName = ".snapshots"
)

// snapshotsRoot is where an app's snapshots live: <apps>/.snapshots/<id>/. Keyed
// on the app's id (like the home) so a rename does not move them.
func (m *Manager) snapshotsRoot(app string) string {
	return filepath.Join(m.config.AppsDir, snapshotsDirName, m.appID(app))
}

// snapshotPath is one snapshot's subvolume path.
func (m *Manager) snapshotPath(app, id string) string {
	return filepath.Join(m.snapshotsRoot(app), id)
}
