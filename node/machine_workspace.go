package node

import (
	"heckel.io/hostit/workspace"
)

// containerName and unitName are keyed on the app's stable id, not its name, so a
// rename never recreates the (stateful) container or its unit. Callers holding
// only a name go through the method, which resolves the id; callers holding the
// app use workspace.ContainerName / workspace.UnitName directly.
func (m *Machine) ContainerName(name string) string {
	return workspace.ContainerName(m.AppID(name))
}

func (m *Machine) UnitName(name string) string {
	return workspace.UnitName(m.AppID(name))
}

// EnsureWorkspaceBase builds the current workspace image (if needed) and exports
// its base rootfs subvolume; the lifecycle lives in the workspace Service, this
// is the Manager's handle on it for the daemon's startup path.
func (m *Machine) EnsureWorkspaceBase() error {
	return m.workspace.EnsureBase(workspace.ImageTag())
}

// PruneOldWorkspaceImages removes workspace images and base subvolumes that are
// neither current nor pinned by an app; like EnsureWorkspaceBase, a thin handle
// on the workspace Service for the daemon's startup path.
func (m *Machine) PruneOldWorkspaceImages() {
	m.workspace.PruneOldImages()
	m.workspace.PruneOldBases()
}
