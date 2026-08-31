package node

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/node/api"
	"heckel.io/hostit/system/relaypaths"
)

// The SSH-relay frontend. Control pushes this node its routing table, known_hosts
// and per-app authorized_keys over the cluster link (ApplyRelay); the node writes
// them to the fixed relaypaths its own hostit-relay helper and stub reconcile
// read, then keeps the frontend stub accounts matching. Nothing is shared with
// control through the filesystem, so the frontend can live on a different host.

// Relay frontend paths as package vars (not consts) so a test can point them at
// a temp dir. Defaults are the shared relaypaths, which the hostit-relay helper
// reads too.
var (
	relayRoutesPath     = relaypaths.Routes
	relayKnownHostsPath = relaypaths.KnownHosts
	relayKeysPath       = relaypaths.Keys
	relayStubsPath      = relaypaths.Stubs
	relayPubKeyPath     = relaypaths.PubKey
)

// isRelayFrontend reports whether this node is the relay frontend: the deploy put
// a relay key here. Only a frontend reconciles stubs, reports its relay pubkey,
// and accepts an ApplyRelay push.
func (m *Machine) isRelayFrontend() bool {
	_, err := os.Stat(relayPubKeyPath)
	return err == nil
}

// relayPubKey returns this frontend's relay public key (one line), reported to
// control so it can add the key to remote apps' authorized_keys. Empty on a
// non-frontend node.
func (m *Machine) relayPubKey() string {
	data, err := os.ReadFile(relayPubKeyPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ApplyRelay writes the routing table, known_hosts and per-app authorized_keys
// control computed, then reconciles the frontend stubs. Called over the cluster
// link on every relay change, so the frontend needs no shared filesystem with
// control. A no-op on a node that is not a relay frontend.
func (m *Machine) ApplyRelay(spec *api.RelaySpec) error {
	if spec == nil || !m.isRelayFrontend() {
		return nil
	}
	if err := writeFileAtomic(relayRoutesPath, spec.Routes, 0644); err != nil {
		return err
	}
	if err := writeFileAtomic(relayKnownHostsPath, spec.KnownHosts, 0644); err != nil {
		return err
	}
	if err := m.writeRelayKeys(spec.AppKeys); err != nil {
		return err
	}
	m.ReconcileRelayStubs()
	return nil
}

// writeRelayKeys writes each routed app's authorized_keys to a per-app file the
// stub reconcile reads, and removes files for apps no longer routed.
func (m *Machine) writeRelayKeys(appKeys map[string]string) error {
	if err := os.MkdirAll(relayKeysPath, 0755); err != nil {
		return err
	}
	for app, keys := range appKeys {
		if !api.ValidName(app) {
			continue // never let an unexpected name reach a root-level path/user op
		}
		if err := writeFileAtomic(filepath.Join(relayKeysPath, app), keys, 0644); err != nil {
			slog.Warn("Cannot write a relay keys file", "app", app, "error", err)
		}
	}
	entries, err := os.ReadDir(relayKeysPath)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if _, ok := appKeys[e.Name()]; !ok && !strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(relayKeysPath, e.Name()))
		}
	}
	return nil
}

// writeFileAtomic writes content to path via a temp file and rename, creating
// the parent directory first.
func writeFileAtomic(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReconcileRelayStubs makes the frontend's stub accounts match the relay routes.
// For every routed (remote) app it ensures a stub user exists with the app's
// current authorized_keys; stubs for apps no longer routed are removed. It is
// deliberately ISOLATED from the app reconcile: it only ever touches users homed
// under relaypaths.Stubs, so a bug here cannot harm a real app. A no-op on a node
// that is not a relay frontend.
func (m *Machine) ReconcileRelayStubs() {
	if !m.isRelayFrontend() {
		return
	}
	routed := readRoutedApps(relayRoutesPath)

	// Ensure a stub + current keys for each routed app.
	for app := range routed {
		if !api.ValidName(app) {
			continue // a stub is a real Unix user; never create one from an unexpected name
		}
		if err := m.ensureRelayStub(app); err != nil {
			slog.Warn("Cannot ensure relay stub", "app", app, "error", err)
		}
	}
	// Remove stubs (users homed under relaypaths.Stubs) for apps no longer routed.
	accounts, err := m.user.List()
	if err != nil {
		return
	}
	prefix := filepath.Clean(relayStubsPath) + string(filepath.Separator)
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
		_ = os.RemoveAll(filepath.Join(relayStubsPath, a.Name))
	}
}

// ensureRelayStub creates the stub account if missing and writes its
// authorized_keys from the per-app keys file. All files are root-owned (the
// tenant cannot alter their own frontend authorized_keys); root ownership plus
// no group/world write satisfies sshd StrictModes.
func (m *Machine) ensureRelayStub(app string) error {
	home := filepath.Join(relayStubsPath, app)
	if !m.user.Exists(app) {
		if err := m.user.CreateStub(app, home); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	// Suppress the frontend host's MOTD on an interactive relay login (it would
	// otherwise leak the control host's banner and stats to the tenant).
	_ = os.WriteFile(filepath.Join(home, ".hushlogin"), nil, 0644)
	keys, _ := os.ReadFile(filepath.Join(relayKeysPath, app)) // absent -> deny all
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
// control pushes; short so a revoked key stops working within seconds.
const relayStubInterval = 3 * time.Second

// RelayStubLoop reconciles the frontend relay stubs until done closes. A no-op
// (cheap stat) on a node that is not a relay frontend.
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
