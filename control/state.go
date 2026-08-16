package control

import (
	"log/slog"
	"time"

	"heckel.io/hostit/appconf"
)

const (
	// stateTTL is how old the control cache may get before a read kicks off a refresh
	stateTTL = 10 * time.Second
)

// CachedStates returns the last known state of the given apps immediately and,
// when the cache has aged out, kicks off a refresh in the background. Listing
// apps therefore never waits on podman or systemd: the page renders at once and
// picks up exact numbers on the next poll.
func (m *Manager) CachedStates(names []string) map[string]State {
	m.ctlStatesMu.Lock()
	cached := make(map[string]State, len(names))
	unknown := false
	for _, name := range names {
		state, ok := m.ctlStates[name]
		cached[name] = state
		unknown = unknown || !ok
	}
	stale := time.Since(m.ctlStatesFresh) > stateTTL
	m.ctlStatesMu.Unlock()

	// An app the cache has never seen was just created, and its owner is looking
	// at its page right now: waiting out the TTL would show them "stopped" for
	// ten seconds after it started
	if stale || unknown {
		go m.refreshOnce()
	}
	return cached
}

// SeedStates seeds BOTH halves' caches from recorded intent: the control
// plane's (what the UI reads on the first page load) and the machine's (the
// promoted seed below). Shadowing the machine method keeps the one-liner
// callers in control and node correct for whichever half matters in that process.
func (m *Manager) SeedStates() {
	m.Machine.SeedStates()
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps to seed states", "error", err)
		return
	}
	m.ctlStatesMu.Lock()
	defer m.ctlStatesMu.Unlock()
	for _, a := range apps {
		if _, ok := m.ctlStates[a.Name]; ok {
			continue
		}
		state := State{}
		if !a.PoweredOff {
			state = State{Running: true, AppRunning: true, AppState: appconf.AppStateRunning}
		}
		m.ctlStates[a.Name] = state
	}
}

// refreshOnce refreshes the control cache unless one is already in flight;
// its single-flight guard is the control side's own, distinct from the
// machine's measurement guard.
func (m *Manager) refreshOnce() {
	m.ctlStatesMu.Lock()
	if m.ctlRefreshing {
		m.ctlStatesMu.Unlock()
		return
	}
	m.ctlRefreshing = true
	m.ctlStatesMu.Unlock()
	defer func() {
		m.ctlStatesMu.Lock()
		m.ctlRefreshing = false
		m.ctlStatesMu.Unlock()
	}()
	m.RefreshStates()
}

// RefreshStates measures every app and replaces the cache; it blocks on podman,
// so only background work should call it
func (m *Manager) RefreshStates() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps for state refresh", "error", err)
		return
	}
	names := make([]string, 0, len(apps))
	for _, a := range apps {
		names = append(names, a.Name)
	}
	// Measure through the node agent: in a single process it is this Manager
	// (local podman/systemd), but in split control mode it is the routing agent,
	// which fans out to the nodes -- control has no app containers of its own to
	// measure, and doing so would clobber the per-node poll data with empties.
	states := m.node.States(names)
	m.ctlStatesMu.Lock()
	m.ctlStates = states
	m.ctlStatesFresh = time.Now()
	m.ctlStatesMu.Unlock()
}

// IngestStates replaces the state cache with externally measured states: what
// control does in split mode, where the node measures and reports.
func (m *Manager) IngestStates(states map[string]State) {
	m.ctlStatesMu.Lock()
	defer m.ctlStatesMu.Unlock()
	// Merge per name: in multi-node mode each node's poll loop feeds only its
	// own apps, and a whole-map swap would clobber the other nodes' entries.
	if m.ctlStates == nil {
		m.ctlStates = make(map[string]State, len(states))
	}
	for name, state := range states {
		m.ctlStates[name] = state
	}
	m.ctlStatesFresh = time.Now()
}
