package control

import (
	"errors"
	"log/slog"

	"heckel.io/hostit/store"
)

// RenameApp changes an app's name. Because every durable resource -- home,
// snapshots, container, systemd unit, tokens and the other per-app rows -- keys on
// the app's stable id, the rename itself moves nothing and the container is NOT
// recreated: an app keeps whatever state it built up in its subvolume (its
// files and everything it installed).
//
// The machine half (stopping the unit, the usermod, the cache carry-over) runs
// through the node agent on the app's OWN node -- locally when fused, over the
// node RPC in split mode. The registry flip happens here afterwards; when it
// loses the race to a same-name create, the machine rename is compensated by
// renaming back, so login and registry stay consistent.
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
	if err := m.node.Rename(oldName, newName, a.ID); err != nil {
		return nil, err
	}
	if err := m.store.RenameApp(oldName, newName); err != nil {
		if renameErr := m.node.Rename(newName, oldName, a.ID); renameErr != nil {
			slog.Error("Cannot rename app login back after a failed registry flip", "app", oldName, "error", renameErr)
		}
		if errors.Is(err, store.ErrAppNameTaken) {
			return nil, ErrAppExists
		}
		return nil, err
	}
	m.PushMirror()
	return m.store.App(newName)
}
