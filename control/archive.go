package control

import (
	"errors"
	"log/slog"
)

// Archiving: shelving an app instead of deleting it.
//
// An archived app is one nobody wants running but nobody wants gone either. It
// refuses to run at all, stops taking new snapshots, and its history thins to
// monthly rollups (retention.Archived) rather than being kept dense or dropped.
// It is a separate flag from powered_off on purpose: an owner flips power
// freely, so overloading it would make "off because I am done for today"
// indistinguishable from "off because this app is retired".

// ErrArchived is what every verb that would run an archived app returns.
var ErrArchived = errors.New("app is archived; unarchive it first")

// Archive shelves an app: it is powered off, and refuses to come back until it
// is unarchived. Powering off is part of archiving rather than something the
// owner has to remember, since an archived app that kept running would hold its
// memory for nothing.
func (m *Manager) Archive(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	if err := m.node.Down(name); err != nil {
		// Worth recording but not worth refusing: the node may be away, and the
		// archive flag below is what stops the app coming back either way.
		slog.Warn("Cannot power off an app being archived", "app", name, "error", err)
	}
	return m.store.SetAppArchived(name, true)
}

// Unarchive brings a shelved app back as an ordinary powered-off app. It is
// deliberately not started: coming out of the archive is not the same decision
// as wanting the app running again.
func (m *Manager) Unarchive(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	return m.store.SetAppArchived(name, false)
}

// archived reports whether an app is shelved. A missing app is not archived --
// the caller's own lookup reports that more usefully.
func (m *Manager) archived(name string) bool {
	a, err := m.store.App(name)
	return err == nil && a.Archived
}
