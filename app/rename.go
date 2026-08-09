package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"heckel.io/hostit/store"
)

// RenameApp changes an app's name. Because every durable resource -- home,
// snapshots, container, systemd unit, tokens and the other per-app rows -- keys on
// the app's stable id, the rename itself moves nothing and the container is NOT
// recreated: an app keeps whatever state it built up (installed packages, its
// writable layer).
//
// The one wrinkle is the Unix login: usermod --login refuses while the user has a
// live process, and its container -- plus any open web-terminal or SSH session,
// which run as that user -- is exactly that. So a running app is stopped around the
// rename and started again afterwards. Stop/start reuses the same container, so no
// state is lost; it is a brief blip, not a rebuild. Old <name>.<base> links stop
// resolving; the new one works on the next request.
func (m *Manager) RenameApp(oldName, newName string) (*store.App, error) {
	defer m.lockApp(oldName)()
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
	// The unit is keyed on the (unchanging) id, so the same name stops and starts it.
	unit := unitNameForID(a.ID)
	wasRunning := m.isActive(oldName)
	if wasRunning {
		// Stop the unit first: it is Restart=always, so it must be stopped (not just
		// the container killed) or systemd would immediately bring the app back up
		// and re-block usermod. This also stops the app's own container processes.
		_, _ = m.runner.Run("systemctl", "stop", unit)
	}
	// usermod --login refuses while ANY process runs as the app user: its container,
	// but also an in-container htop/shell started via a web-terminal or SSH session
	// (those exec into the container, so stopping the unit does not reliably reap
	// them in time). With the unit stopped (so nothing restarts), force-kill whatever
	// is left owned by that user. It is session leftovers, not the app.
	_ = m.ops.KillUserProcesses(oldName)
	// startApp brings the app back under its CURRENT name (used on both the failure
	// restores, where the name is still the old one, and success).
	startApp := func() {
		if wasRunning {
			_, _ = m.runner.Run("systemctl", "start", unit)
		}
	}

	// Rename the Unix login (uid and home unchanged). This is the only OS mutation.
	if err := m.renameUser(oldName, newName); err != nil {
		startApp() // bring the app back under its old name
		return nil, fmt.Errorf("cannot rename app user: %w", err)
	}
	// Flip the name in the store; on failure, undo the user rename to stay consistent.
	if err := m.store.RenameApp(oldName, newName); err != nil {
		_ = m.ops.RenameUser(newName, oldName)
		startApp()
		if errors.Is(err, store.ErrAppNameTaken) {
			return nil, ErrAppExists
		}
		return nil, err
	}
	// Carry the name-keyed in-memory caches over so the next lookup is warm.
	m.renameCaches(oldName, newName)
	startApp()
	// The container keeps the --hostname it was created with: podman drops
	// CAP_SYS_ADMIN, so a running container's hostname cannot be changed without
	// recreating it (which would lose its writable layer) or granting a near-root
	// capability. It picks up the new name on the next deploy, which recreates it.
	// The SSH login banner shows the app's current name regardless (it comes from
	// the daemon); only the bare `hostname` command and the shell's \h prompt keep
	// the old name until then.
	return m.store.App(newName)
}

// renameUser renames the app's Unix login, retrying briefly: stopping the app's
// container tears down its sessions asynchronously, so usermod can still see a
// dying process for a moment ("currently used by process ...").
func (m *Manager) renameUser(oldName, newName string) error {
	var err error
	for i := 0; i < 15; i++ {
		if err = m.ops.RenameUser(oldName, newName); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "currently used") {
			return err // a real failure, not the process-death race
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// renameCaches moves an app's name-keyed in-memory state from the old name to the
// new one. Everything durable keys on the id and needs no move.
func (m *Manager) renameCaches(oldName, newName string) {
	m.mu.Lock()
	if v, ok := m.memoryMB[oldName]; ok {
		m.memoryMB[newName] = v
		delete(m.memoryMB, oldName)
	}
	if v, ok := m.diskMB[oldName]; ok {
		m.diskMB[newName] = v
		delete(m.diskMB, oldName)
	}
	m.mu.Unlock()
	m.stateMu.Lock()
	if v, ok := m.stateCache[oldName]; ok {
		m.stateCache[newName] = v
		delete(m.stateCache, oldName)
	}
	m.stateMu.Unlock()
}
