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
)

const (
	// stateSettleInterval is how often to re-measure after a lifecycle action, and
	// stateSettleWindow is for how long. A reboot or an app restart can take several
	// seconds, and the UI polls every couple of seconds waiting for it, so the cache
	// has to keep up for the whole transition rather than just glancing once or twice.
	stateSettleInterval = 2 * time.Second
	stateSettleWindow   = 40 * time.Second
)

// State is what an app is doing right now: whether its container is up, whether
// the run: process inside it is up, and how much memory its container is using.
//
// StartedAt and AppStartedAt are the only way the UI can tell a reboot or an app
// restart apart from "nothing happened yet": both leave the app in the same
// running state it was in before, so the UI waits for a start time newer than the
// one it saw when the action was issued.
type State struct {
	Running      bool  `json:"running"`        // The container's systemd unit is active
	AppRunning   bool  `json:"app_running"`    // The run: command inside it is up
	MemoryMB     int   `json:"memory_mb"`      // Current container memory use in MB
	CPUPercent   int   `json:"cpu_percent"`    // Current container CPU use in whole percent (may exceed 100 on multiple cores)
	StartedAt    int64 `json:"started_at"`     // Unix seconds the container last started (0 if down)
	AppStartedAt int64 `json:"app_started_at"` // Unix millis the run: process last changed state (0 if never)
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
	// Re-measure repeatedly for the length of a transition, not just once: the UI
	// watches the app's start time to know a reboot/restart finished, and it can
	// only see that if the cache is refreshed the whole time the action is settling.
	go func() {
		for elapsed := time.Duration(0); elapsed < stateSettleWindow; elapsed += stateSettleInterval {
			time.Sleep(stateSettleInterval)
			m.refreshOnce()
		}
	}()
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
	starts := m.containerStartTimes(names)
	for name, running := range m.runningStates(names) {
		// The app can only be up if its container is; a stopped or crashed app
		// leaves the container running but reports something other than "running".
		appRunning, appStartedAt := m.appProcessState(name)
		states[name] = State{
			Running:      running,
			AppRunning:   running && appRunning,
			StartedAt:    starts[name],
			AppStartedAt: appStartedAt,
		}
	}
	for name, usage := range m.resourceUsage() {
		state := states[name]
		state.MemoryMB, state.CPUPercent = usage.memoryMB, usage.cpuPercent
		states[name] = state
	}
	return states
}

// usage is one container's live resource consumption, from a single stats call
type usage struct {
	memoryMB   int
	cpuPercent int
}

// appProcessState reads the breadcrumb the agent leaves: whether the run: process
// is up, and the time it last changed state (the file's mtime, bumped on every
// start/stop/crash). The time lets the UI see an app restart, which otherwise
// looks identical to no change. Anything unreadable means "not running", which is
// the safe default (the app is not serving).
func (m *Manager) appProcessState(name string) (running bool, startedAt int64) {
	root, err := m.appRoot(name)
	if err != nil {
		return false, 0
	}
	defer root.Close()
	b, err := readCapped(root, appStateFile, maxStateRead)
	if err != nil {
		return false, 0
	}
	// Milliseconds, not seconds: a restart of an app that started less than a second
	// ago would otherwise land in the same second and look like nothing changed.
	if stat, err := root.Stat(appStateFile); err == nil {
		startedAt = stat.ModTime().UnixMilli()
	}
	return strings.TrimSpace(string(b)) == "running", startedAt
}

// containerStartTimes reports when each app's container last started, as Unix
// seconds; a container that is not running is simply absent. podman prints one
// line per running container, which recreate/restart makes newer -- the signal
// the UI uses to know a reboot actually happened.
func (m *Manager) containerStartTimes(names []string) map[string]int64 {
	starts := make(map[string]int64, len(names))
	out, err := m.runner.RunTimeout(stateTimeout, "podman", "ps", "--format", "{{.Names}}|{{.StartedAt}}")
	if err != nil {
		return starts
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[0], containerPrefix)
		if name == fields[0] {
			continue // Not one of ours
		}
		if ts, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil {
			starts[name] = ts
		}
	}
	return starts
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

// resourceUsage reads current container memory and CPU from one podman stats call
func (m *Manager) resourceUsage() map[string]usage {
	usages := make(map[string]usage)
	out, err := m.runner.RunTimeout(stateTimeout, "podman", "stats", "--no-stream", "--format", "json")
	if err != nil {
		return usages
	}
	// podman prints lowercase snake_case keys, e.g.
	//   {"name":"hostit-app-blog","cpu_percent":"3.70%","mem_usage":"4.633MB / 536.9MB"}
	var stats []struct {
		Name       string `json:"name"`
		MemUsage   string `json:"mem_usage"`
		CPUPercent string `json:"cpu_percent"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		return usages
	}
	for _, stat := range stats {
		name := strings.TrimPrefix(stat.Name, containerPrefix)
		if name == stat.Name {
			continue // Not one of ours
		}
		usages[name] = usage{memoryMB: parseMemMB(stat.MemUsage), cpuPercent: parseCPUPercent(stat.CPUPercent)}
	}
	return usages
}

// parseCPUPercent turns podman's "3.70%" into whole percent, rounded. It can be
// over 100 for a container using more than one core, which is fine to report.
func parseCPUPercent(cpu string) int {
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cpu), "%"))
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int(parsed + 0.5)
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
