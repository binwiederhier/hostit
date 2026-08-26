package node

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// relayStubComment marks the frontend relay-stub accounts (in the app group but
// homed outside the apps pool, so reconcileUsers never reaps them).

// ReconcileRelayStubs makes the frontend's stub accounts match the relay routes
// control writes. For every routed (remote) app it ensures a stub user exists
// with the app's current authorized_keys; stubs for apps no longer routed are
// removed. It is deliberately ISOLATED from the app reconcile: it only ever
// touches users homed under RelayStubsDir, so a bug here cannot harm a real app.
// A no-op when the relay is not configured (empty dirs or missing routes file).
func (m *Machine) ReconcileRelayStubs() {
	if m.config.RelayStubsDir == "" || m.config.SSHRoutesFile == "" {
		return
	}
	routed := readRoutedApps(m.config.SSHRoutesFile)

	// Ensure a stub + current keys for each routed app.
	for app := range routed {
		if err := m.ensureRelayStub(app); err != nil {
			slog.Warn("Cannot ensure relay stub", "app", app, "error", err)
		}
	}
	// Remove stubs (users homed under RelayStubsDir) for apps no longer routed.
	accounts, err := m.user.List()
	if err != nil {
		return
	}
	prefix := filepath.Clean(m.config.RelayStubsDir) + string(filepath.Separator)
	for _, a := range accounts {
		if !strings.HasPrefix(filepath.Clean(a.Home)+string(filepath.Separator), prefix) {
			continue // not a relay stub
		}
		if routed[a.Name] {
			continue
		}
		_ = m.user.KillProcesses(a.Name)
		if err := m.user.Delete(a.Name); err != nil {
			slog.Warn("Cannot remove a stale relay stub", "app", a.Name, "error", err)
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.config.RelayStubsDir, a.Name))
	}
}

// ensureRelayStub creates the stub account if missing and writes its
// authorized_keys from the per-app keys file. All files are root-owned (the
// tenant cannot alter their own frontend authorized_keys); root ownership plus
// no group/world write satisfies sshd StrictModes.
func (m *Machine) ensureRelayStub(app string) error {
	home := filepath.Join(m.config.RelayStubsDir, app)
	if !m.user.Exists(app) {
		if err := m.user.CreateStub(app, home); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	var keys []byte
	if m.config.RelayKeysDir != "" {
		keys, _ = os.ReadFile(filepath.Join(m.config.RelayKeysDir, app)) // absent -> deny all
	}
	akPath := filepath.Join(home, ".ssh", "authorized_keys")
	if cur, err := os.ReadFile(akPath); err == nil && string(cur) == string(keys) {
		return nil // unchanged
	}
	tmp := akPath + ".tmp"
	if err := os.WriteFile(tmp, keys, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, akPath)
}

// readRoutedApps returns the set of apps in the routes file (one "app<TAB>host"
// line each). Missing file -> empty set (which removes every stub).
func readRoutedApps(path string) map[string]bool {
	routed := make(map[string]bool)
	data, err := os.ReadFile(path)
	if err != nil {
		return routed
	}
	for _, line := range strings.Split(string(data), "\n") {
		if app, _, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && app != "" {
			routed[app] = true
		}
	}
	return routed
}

// relayStubInterval keeps the frontend stub accounts close to the routes/keys
// control writes; short so a revoked key stops working within seconds.
const relayStubInterval = 3 * time.Second

// RelayStubLoop reconciles the frontend relay stubs until done closes.
func (m *Machine) RelayStubLoop(done <-chan struct{}) {
	m.ReconcileRelayStubs()
	for {
		select {
		case <-done:
			return
		case <-time.After(relayStubInterval):
			m.ReconcileRelayStubs()
		}
	}
}
