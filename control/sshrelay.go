package control

import (
	"log/slog"
	"sort"
	"strings"

	"heckel.io/hostit/node/api"
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

// buildRelaySpec computes what control pushes to a relay-frontend node: the
// routing table, known_hosts, and each routed (remote) app's authorized_keys
// (the real users -- the relay key is added on the node hop, not here). The
// frontend's own mirror is filtered to its apps, so it cannot compute this
// itself; control does and pushes it over the link.
func (m *Manager) buildRelaySpec() (*api.RelaySpec, error) {
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	nodes, err := m.store.Nodes()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.Name] = n
	}
	routes, knownHosts := sshRelayFiles(apps, byID)
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
	return &api.RelaySpec{Routes: routes, KnownHosts: knownHosts, AppKeys: appKeys}, nil
}

// refreshSSHRelay recomputes the relay spec and pushes it to every connected
// frontend node, best-effort: a failure logs but never fails the placement
// operation that triggered it. A no-op unless the relay is enabled.
func (m *Manager) refreshSSHRelay() {
	if !m.config.SSHRelayEnabled {
		return
	}
	ids := m.relayFrontendIDs()
	if len(ids) == 0 {
		return // no frontend has connected yet; it gets the spec on connect
	}
	spec, err := m.buildRelaySpec()
	if err != nil {
		slog.Warn("Cannot build SSH relay spec", "error", err)
		return
	}
	for _, id := range ids {
		agent := m.registry.Agent(id)
		if agent == nil {
			continue
		}
		if err := agent.ApplyRelay(spec); err != nil {
			slog.Warn("Cannot push SSH relay spec", "node", id, "error", err)
		}
	}
}

// recordRelayFrontend notes that a node is a relay frontend and caches the
// authorized_keys line for its relay key, added to remote apps' keys so the
// frontend can ssh in as the app user. Reporting the key replaces control
// reading it off a shared filesystem. Returns whether the line changed, so the
// caller can re-reconcile remote nodes that synced before the key was known.
func (m *Manager) recordRelayFrontend(nodeID, pubKey string) bool {
	m.relayLineMu.Lock()
	defer m.relayLineMu.Unlock()
	if m.relayFrontends == nil {
		m.relayFrontends = make(map[string]bool)
	}
	m.relayFrontends[nodeID] = true
	line := "restrict,pty " + strings.TrimSpace(pubKey)
	changed := line != m.relayLine
	m.relayLine = line
	return changed
}

// resyncRelayKeyToNodes re-pushes desired state to every connected non-frontend
// node, so a remote app whose keys were synced before the frontend reported its
// relay key gets the key added. Called only when the relay line first appears or
// changes -- rare -- not on every refresh.
func (m *Manager) resyncRelayKeyToNodes() {
	nodes, err := m.store.Nodes()
	if err != nil {
		return
	}
	m.relayLineMu.Lock()
	frontends := m.relayFrontends
	m.relayLineMu.Unlock()
	for _, n := range nodes {
		if frontends[n.Name] {
			continue // a frontend's own apps are colocated; they get no relay key
		}
		agent := m.registry.Agent(n.Name)
		if agent == nil {
			continue
		}
		if desired, err := m.DesiredState(n.Name); err == nil {
			agent.Reconcile(desired)
		}
	}
}

// relayFrontendIDs returns the node ids that have reported being relay frontends.
func (m *Manager) relayFrontendIDs() []string {
	m.relayLineMu.Lock()
	defer m.relayLineMu.Unlock()
	ids := make([]string, 0, len(m.relayFrontends))
	for id := range m.relayFrontends {
		ids = append(ids, id)
	}
	return ids
}

// relayKeyLine is the authorized_keys line for the relay key, added to REMOTE
// apps' authorized_keys so the frontend can ssh in as the app user. Empty when
// the relay is off, or until a frontend node has reported its relay public key.
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
