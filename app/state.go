package app

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	// stateTimeout bounds the commands behind a state refresh. podman serializes
	// on its own lock, so a create or pull in flight can take minutes.
	stateTimeout = 2 * time.Second
	// stateTTL is how old the cache may get before a read kicks off a refresh
	stateTTL = 10 * time.Second
	// stateRefreshInterval keeps the cache warm while nobody is looking, so the
	// first page load after a quiet period is not the one that pays
	stateRefreshInterval = 30 * time.Second
	// logsTimeout bounds "podman logs", which an agent asks for right after a
	// deploy -- exactly when another app's build may be holding podman's lock
	logsTimeout = 5 * time.Second
)

var (
	// stateSettleDelays is when to look again after a lifecycle action, while the
	// unit is still making up its mind about being active
	stateSettleDelays = []time.Duration{2 * time.Second, 6 * time.Second}
)

// State is what an app is doing right now: whether its service is up and how
// much memory its container is using
type State struct {
	Running  bool `json:"running"`
	MemoryMB int  `json:"memory_mb"`
}

// CachedStates returns the last known state of the given apps immediately and,
// when the cache has aged out, kicks off a refresh in the background. Listing
// apps therefore never waits on podman or systemd: the page renders at once and
// picks up exact numbers on the next poll.
func (m *Manager) CachedStates(names []string) map[string]State {
	m.stateMu.Lock()
	cached := make(map[string]State, len(names))
	unknown := false
	for _, name := range names {
		state, ok := m.stateCache[name]
		cached[name] = state
		unknown = unknown || !ok
	}
	stale := time.Since(m.stateFresh) > stateTTL
	m.stateMu.Unlock()

	// An app the cache has never seen was just created, and its owner is looking
	// at its page right now: waiting out the TTL would show them "stopped" for
	// ten seconds after it started
	if stale || unknown {
		go m.refreshOnce()
	}
	return cached
}

// stateChanged is called when an app was just started, stopped or replaced. It
// drops what the cache knew, and looks again shortly after.
//
// Forgetting alone is not enough: "systemctl start" returns before the unit
// reports active, so an immediate measurement records "stopped" and the cache
// then serves that confidently for a whole TTL. The owner is watching the status
// dot while this happens, so the answer has to catch up in seconds, not tens of
// seconds.
func (m *Manager) stateChanged(name string) {
	m.stateMu.Lock()
	delete(m.stateCache, name)
	m.stateMu.Unlock()
	for _, delay := range stateSettleDelays {
		go func() {
			time.Sleep(delay)
			m.refreshOnce()
		}()
	}
}

// beginRefresh claims the right to refresh, so only one runs at a time. Two
// concurrent refreshes would ask podman twice and, worse, the slower one would
// write last -- stamping older numbers with a newer freshness time.
func (m *Manager) beginRefresh() bool {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.stateRefreshing {
		return false
	}
	m.stateRefreshing = true
	return true
}

func (m *Manager) doneRefreshing() {
	m.stateMu.Lock()
	m.stateRefreshing = false
	m.stateMu.Unlock()
}

// refreshOnce refreshes unless a refresh is already in flight
func (m *Manager) refreshOnce() {
	if !m.beginRefresh() {
		return
	}
	defer m.doneRefreshing()
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
	states := m.States(names)
	m.stateMu.Lock()
	m.stateCache = states
	m.stateFresh = time.Now()
	m.stateMu.Unlock()
}

// StateLoop keeps the cache warm until done closes
func (m *Manager) StateLoop(done <-chan struct{}) {
	slog.Info("Starting app state loop", "interval", stateRefreshInterval)
	defer slog.Info("Stopping app state loop")
	m.refreshOnce() // Prime it, so the first page load already has numbers
	for {
		select {
		case <-time.After(stateRefreshInterval):
		case <-done:
			return
		}
		m.refreshOnce()
	}
}

// States measures the given apps. Both podman and systemd are asked once for
// all of them rather than once per app, so the cost does not grow with the
// number of apps.
func (m *Manager) States(names []string) map[string]State {
	states := make(map[string]State, len(names))
	if len(names) == 0 {
		return states
	}
	for name, running := range m.runningStates(names) {
		states[name] = State{Running: running}
	}
	for name, memoryMB := range m.memoryUsage() {
		state := states[name]
		state.MemoryMB = memoryMB
		states[name] = state
	}
	return states
}

// runningStates asks systemd about every app's unit in one call; "systemctl
// is-active" prints one line per unit, in order
func (m *Manager) runningStates(names []string) map[string]bool {
	args := []string{"systemctl", "is-active"}
	for _, name := range names {
		args = append(args, unitName(name))
	}
	out, _ := m.runner.RunTimeout(stateTimeout, args...) // Non-zero exit just means "something is inactive"
	lines := strings.Split(strings.TrimSpace(out), "\n")
	running := make(map[string]bool, len(names))
	for i, name := range names {
		running[name] = i < len(lines) && strings.TrimSpace(lines[i]) == "active"
	}
	return running
}

// memoryUsage reads current container memory from one podman stats call
func (m *Manager) memoryUsage() map[string]int {
	usage := make(map[string]int)
	out, err := m.runner.RunTimeout(stateTimeout, "podman", "stats", "--no-stream", "--format", "json")
	if err != nil {
		return usage
	}
	// podman prints lowercase snake_case keys, e.g.
	//   {"name":"hostit-app-blog","mem_usage":"4.633MB / 536.9MB"}
	var stats []struct {
		Name     string `json:"name"`
		MemUsage string `json:"mem_usage"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		return usage
	}
	for _, stat := range stats {
		name := strings.TrimPrefix(stat.Name, containerPrefix)
		if name == stat.Name {
			continue // Not one of ours
		}
		usage[name] = parseMemMB(stat.MemUsage)
	}
	return usage
}

// parseMemMB turns podman's "12.3MB / 512MB" into whole megabytes
func parseMemMB(memUsage string) int {
	value := strings.TrimSpace(strings.Split(memUsage, "/")[0])
	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, "GB"):
		multiplier, value = 1024, strings.TrimSuffix(value, "GB")
	case strings.HasSuffix(value, "MB"):
		multiplier, value = 1, strings.TrimSuffix(value, "MB")
	case strings.HasSuffix(value, "kB"):
		multiplier, value = 1.0/1024, strings.TrimSuffix(value, "kB")
	case strings.HasSuffix(value, "B"):
		multiplier, value = 1.0/(1024*1024), strings.TrimSuffix(value, "B")
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return int(parsed * multiplier)
}
