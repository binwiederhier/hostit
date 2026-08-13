package app

import (
	"heckel.io/hostit/workspace"
)

// containerName and unitName are keyed on the app's stable id, not its name, so a
// rename never recreates the (stateful) container or its unit. Callers holding
// only a name go through the method, which resolves the id; callers holding the
// app use workspace.ContainerName / workspace.UnitName directly.
func (m *Manager) containerName(name string) string {
	return workspace.ContainerName(m.appID(name))
}

func (m *Manager) unitName(name string) string {
	return workspace.UnitName(m.appID(name))
}

// EnsureWorkspaceImage builds the workspace image unless it already exists; the
// image lifecycle lives in the workspace Service, this is the Manager's handle
// on it for the daemon's startup path.
func (m *Manager) EnsureWorkspaceImage() error {
	return m.workspace.EnsureImage()
}

// PruneOldWorkspaceImages removes workspace images that are neither the current
// one nor pinned by an app; like EnsureWorkspaceImage, a thin handle on the
// workspace Service for the daemon's startup path.
func (m *Manager) PruneOldWorkspaceImages() {
	m.workspace.PruneOldImages()
}
