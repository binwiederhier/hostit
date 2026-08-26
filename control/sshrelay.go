package control

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"heckel.io/hostit/store"
)

// sshRelayFiles computes the ssh-routes and relay_known_hosts contents for the
// relay gateway from the current apps and nodes. Pure, so it is unit-tested
// without disk. Only a REMOTE app (hosted off the control node) gets a route:
// a colocated app's node IS the frontend, so it needs no relay. Each remote
// node that hosts a routed app and has reported both its SSH host and host key
// gets one known_hosts line.
func sshRelayFiles(apps []*store.App, nodeByID map[string]*store.Node) (routes, knownHosts string) {
	var routeLines []string
	usedNodes := make(map[string]bool)
	for _, a := range apps {
		if a.Host == "" || a.Host == store.HostLocal {
			continue // colocated: apps.<domain> IS its node, no relay needed
		}
		n := nodeByID[a.Host]
		if n == nil || n.SSHHost == "" {
			continue // node unknown or not yet reporting a reachable host
		}
		routeLines = append(routeLines, a.Name+"\t"+n.SSHHost)
		usedNodes[a.Host] = true
	}
	sort.Strings(routeLines)

	var khLines []string
	for id := range usedNodes {
		n := nodeByID[id]
		if n.HostKey == "" {
			continue
		}
		khLines = append(khLines, n.SSHHost+" "+n.HostKey)
	}
	sort.Strings(khLines)

	return joinLines(routeLines), joinLines(khLines)
}

// joinLines joins lines with newlines and a trailing newline, or "" if empty.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// WriteSSHRelayFiles regenerates the relay gateway's routing and known_hosts
// files from the store. A no-op unless the relay is enabled. Called on every
// placement change and node report, so a crashed control leaves correct files
// on disk for the relay shell to read.
func (m *Manager) WriteSSHRelayFiles() error {
	if !m.config.SSHRelayEnabled {
		return nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return err
	}
	nodes, err := m.store.Nodes()
	if err != nil {
		return err
	}
	byID := make(map[string]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.Name] = n
	}
	routes, knownHosts := sshRelayFiles(apps, byID)
	if err := atomicWriteFile(m.config.SSHRelayRoutesFile, routes, 0644); err != nil {
		return err
	}
	if err := atomicWriteFile(m.config.SSHRelayKnownHostsFile, knownHosts, 0644); err != nil {
		return err
	}
	return m.writeRelayKeyFiles(apps, byID)
}

// writeRelayKeyFiles writes each remote app's user authorized_keys to a per-app
// file the frontend stub accounts serve (the frontend authenticates the real
// user; the relay key is added only on the node hop, not here). Keys files for
// apps that are no longer remote are removed.
func (m *Manager) writeRelayKeyFiles(apps []*store.App, byID map[string]*store.Node) error {
	dir := m.config.SSHRelayKeysDir
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	wanted := make(map[string]bool)
	for _, a := range apps {
		if a.Host == "" || a.Host == store.HostLocal {
			continue
		}
		if n := byID[a.Host]; n == nil || n.SSHHost == "" {
			continue
		}
		keys, _, _ := m.appPolicy(a)
		content := ""
		if len(keys) > 0 {
			content = strings.Join(keys, "\n") + "\n"
		}
		if err := atomicWriteFile(filepath.Join(dir, a.Name), content, 0644); err != nil {
			slog.Warn("Cannot write a relay keys file", "app", a.Name, "error", err)
			continue
		}
		wanted[a.Name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !wanted[e.Name()] && !strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// refreshSSHRelay rewrites the relay files best-effort; a failure logs but never
// fails the placement operation that triggered it.
func (m *Manager) refreshSSHRelay() {
	if err := m.WriteSSHRelayFiles(); err != nil {
		slog.Warn("Cannot write SSH relay files", "error", err)
	}
}

// atomicWriteFile writes content to path via a temp file and rename, creating
// the parent directory. An empty path is a no-op.
func atomicWriteFile(path, content string, mode os.FileMode) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// relayKeyLine is the authorized_keys line for the relay key, added to REMOTE
// apps' authorized_keys so the frontend can ssh in as the app user. Empty if the
// relay is off or the public key cannot be read. Read once and cached.
func (m *Manager) relayKeyLine() string {
	if !m.config.SSHRelayEnabled || m.config.SSHRelayPublicKeyFile == "" {
		return ""
	}
	m.relayLineMu.Lock()
	defer m.relayLineMu.Unlock()
	if m.relayLine != "" {
		return m.relayLine // read once, successfully -- the key does not change under us
	}
	// Deliberately do NOT cache a failure: control can start before the deploy
	// has generated the key, and a permanent empty cache would drop the relay
	// key from every node's authorized_keys until the next restart.
	data, err := os.ReadFile(m.config.SSHRelayPublicKeyFile)
	if err != nil {
		return ""
	}
	pub := strings.TrimSpace(string(data))
	if pub == "" {
		return ""
	}
	opts := "restrict,pty"
	if from := m.config.SSHRelayFromAddress; from != "" {
		opts = "from=\"" + from + "\"," + opts
	}
	m.relayLine = opts + " " + pub
	return m.relayLine
}
