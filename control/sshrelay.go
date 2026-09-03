package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"

	"heckel.io/hostit/store"
	"heckel.io/hostit/system/relay"
)

// The SSH relay frontend is hostit-control. Control computes the routing table,
// known_hosts and each remote app's authorized_keys, then applies them locally
// through the root relay-sync helper (which also owns the relay keypair and the
// stub accounts). No node is involved: a [control, proxy] host relays without
// running any container machinery.

// relaySyncHelper is the sudo-gated wrapper control drives to reconcile the
// frontend as root. It reads a relay.Spec on stdin and prints the relay pubkey.
const relaySyncHelper = "/usr/lib/hostit/bin/hostit-relay-sync"

// sudoRelaySync is the production relayApply: it hands the spec to the root
// helper over sudo and returns the relay public key the helper reports.
func sudoRelaySync(spec *relay.Spec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("sudo", "-n", relaySyncHelper)
	cmd.Stdin = bytes.NewReader(data)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("relay-sync failed: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// sshRelayFiles computes the ssh-routes and relay_known_hosts contents for the
// frontend from the current apps and nodes. Pure, so it is unit-tested without
// disk. Only a REMOTE app (hosted off the control host) gets a route: a
// colocated app's node IS reachable at apps.<domain>, so it needs no relay stub.
// Each remote node that hosts a routed app and reports both its SSH host and host
// key gets one known_hosts line.
func sshRelayFiles(apps []*store.App, nodeByID map[string]*store.Node) (routes, knownHosts string, skipped []string) {
	var routeLines []string
	usedNodes := make(map[string]bool)
	for _, a := range apps {
		if a.Host == "" || a.Host == store.HostLocal {
			continue // colocated: apps.<domain> IS its node, no relay needed
		}
		n := nodeByID[a.Host]
		if n == nil || n.SSHHost == "" {
			// A remote app whose node is unknown or reports no ssh-host gets no
			// route, known_hosts line or authorized_keys -- yet sshHostFor still
			// advertises a plausible ssh command for it (it falls back to the base
			// domain). Report it so the caller can warn, rather than dropping it in
			// silence and leaving the user with a command that fails at connect.
			skipped = append(skipped, a.Name)
			continue
		}
		routeLines = append(routeLines, a.Name+"\t"+n.SSHHost)
		usedNodes[a.Host] = true
	}
	sort.Strings(routeLines)
	sort.Strings(skipped)

	var khLines []string
	for id := range usedNodes {
		n := nodeByID[id]
		if n.HostKey == "" {
			continue
		}
		khLines = append(khLines, n.SSHHost+" "+n.HostKey)
	}
	sort.Strings(khLines)

	return joinLines(routeLines), joinLines(khLines), skipped
}

// joinLines joins lines with newlines and a trailing newline, or "" if empty.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// buildRelaySpec computes what control applies to the frontend: the routing
// table, known_hosts, and each routed (remote) app's authorized_keys (the real
// users; the relay key is added on the node hop, not here).
func (m *Manager) buildRelaySpec() (*relay.Spec, []string, error) {
	apps, err := m.store.Apps()
	if err != nil {
		return nil, nil, err
	}
	nodes, err := m.store.Nodes()
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.Name] = n
	}
	routes, knownHosts, skipped := sshRelayFiles(apps, byID)
	appKeys := make(map[string]string)
	for _, a := range apps {
		if a.Host == "" || a.Host == store.HostLocal {
			continue
		}
		if n := byID[a.Host]; n == nil || n.SSHHost == "" {
			continue
		}
		keys, _, _ := m.appPolicy(a)
		if len(keys) > 0 {
			appKeys[a.Name] = strings.Join(keys, "\n") + "\n"
		} else {
			appKeys[a.Name] = ""
		}
	}
	return &relay.Spec{Routes: routes, KnownHosts: knownHosts, AppKeys: appKeys}, skipped, nil
}

// refreshSSHRelay recomputes the relay spec and applies it locally through the
// root helper, best-effort: a failure logs but never fails the placement
// operation that triggered it. A no-op unless the relay is enabled. The helper
// also ensures the relay key on first run, so the key exists before the first
// remote app needs it in its authorized_keys.
func (m *Manager) refreshSSHRelay() {
	if !m.config.SSHRelayEnabled {
		return
	}
	spec, skipped, err := m.buildRelaySpec()
	if err != nil {
		slog.Warn("Cannot build SSH relay spec", "error", err)
		return
	}
	if len(skipped) > 0 {
		// These apps are advertised a working-looking ssh command (sshHostFor
		// falls back to the base domain) but have no relay route, so connecting
		// fails with a generic publickey error. The cause -- their node never
		// reported ssh-host -- is invisible without this line.
		slog.Warn("SSH relay excludes apps whose node reports no ssh-host; their advertised ssh command will fail until the node sets ssh-host",
			"apps", skipped)
	}
	pub, err := m.relayApply(spec)
	if err != nil {
		slog.Warn("Cannot apply SSH relay spec", "error", err)
		return
	}
	m.setRelayLine(pub)
}

// setRelayLine records the frontend's relay pubkey as an authorized_keys line.
// When it first appears (or changes), remote apps that synced without it are
// re-keyed so the frontend can ssh in as them.
func (m *Manager) setRelayLine(pubKey string) {
	pubKey = strings.TrimSpace(pubKey)
	line := ""
	if pubKey != "" {
		line = "restrict,pty " + pubKey
	}
	m.relayLineMu.Lock()
	changed := line != m.relayLine
	m.relayLine = line
	m.relayLineMu.Unlock()
	if changed && line != "" {
		m.rekeyRemoteApps()
	}
}

// rekeyRemoteApps re-pushes desired state to every connected node so a remote
// app whose keys were synced before the relay key was known gets it added.
// Called only when the relay line first appears or changes -- rare.
func (m *Manager) rekeyRemoteApps() {
	if m.registry == nil {
		return
	}
	for _, id := range m.registry.IDs() {
		agent := m.registry.Agent(id)
		if agent == nil {
			continue
		}
		if desired, err := m.DesiredState(id); err == nil {
			agent.Reconcile(desired)
		}
	}
}

// relayKeyLine is the authorized_keys line for the relay key, added to REMOTE
// apps' authorized_keys so the frontend can ssh in as the app user. Empty when
// the relay is off, or until the first refresh has generated the key.
func (m *Manager) relayKeyLine() string {
	if !m.config.SSHRelayEnabled {
		return ""
	}
	m.relayLineMu.Lock()
	defer m.relayLineMu.Unlock()
	return m.relayLine
}

// appendRelayKey appends the relay key line to a REMOTE app's key set (the
// frontend must be able to ssh in as the app user). Used by BOTH the desired
// state (mirror) and the explicit SetKeys path, so a key resync cannot drop it.
func (m *Manager) appendRelayKey(host string, keys []string) []string {
	if host == "" || host == store.HostLocal {
		return keys
	}
	line := m.relayKeyLine()
	if line == "" {
		return keys
	}
	return append(append([]string{}, keys...), line)
}
