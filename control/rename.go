package control

import (
	"errors"

	"heckel.io/hostit/store"
)

// RenameApp changes an app's name. Because every durable resource -- home,
// snapshots, container, systemd unit, tokens and the other per-app rows -- keys on
// the app's stable id, the rename itself moves nothing and the container is NOT
// recreated: an app keeps whatever state it built up in its subvolume (its
// files and everything it installed).
//
// The machine half of the rename (stopping the unit, the usermod, the cache
// carry-over) runs through RenameLogin on this process's machine; the registry
// flip is the callback in the middle. NOTE: like the whole rename flow, this
// assumes the app's machine is THIS host -- the known colocated-only gap.
func (m *Manager) RenameApp(oldName, newName string) (*store.App, error) {
	defer m.LockApp(oldName)()
	if newName == oldName {
		return m.store.App(oldName)
	}
	a, err := m.store.App(oldName)
	if err != nil {
		return nil, err // ErrAppNotFound
	}
	// Validate the new name exactly as create does: charset, reserved words, and
	// that no app or Unix user already holds it.
	if err := m.validateName(newName); err != nil {
		return nil, err
	}
	if err := m.RenameLogin(oldName, newName, a.ID, func() error {
		return m.store.RenameApp(oldName, newName)
	}); err != nil {
		if errors.Is(err, store.ErrAppNameTaken) {
			return nil, ErrAppExists
		}
		return nil, err
	}
	m.PushMirror()
	return m.store.App(newName)
}
