package app

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/container"
	"heckel.io/hostit/workspace"
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
	Running    bool `json:"running"`     // The container's systemd unit is active
	AppRunning bool `json:"app_running"` // The run: command inside it is up
	// AppState is the agent's breadcrumb verbatim ("running"/"crashed"/"failed"/... or
	// "" when the container is down), so the UI can tell a crashed give-up from a stop.
	AppState     string `json:"app_state"`
	MemoryMB     int    `json:"memory_mb"`      // Current container memory use in MB
	CPUPercent   int    `json:"cpu_percent"`    // Current container CPU use in whole percent (may exceed 100 on multiple cores)
	StartedAt    int64  `json:"started_at"`     // Unix seconds the container last started (0 if down)
	AppStartedAt int64  `json:"app_started_at"` // Unix millis the run: process last changed state (0 if never)
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
		appState, appStartedAt := m.appProcessState(name)
		states[name] = State{
			Running:      running,
			AppRunning:   running && appState == appctl.AppStateRunning,
			AppState:     appStateFor(running, appState),
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

// appProcessState reads the breadcrumb the agent leaves: the run: process state
// ("running", "crashed", "failed", "stopped", "idle") and the time it last changed
// (the file's mtime, bumped on every start/stop/crash). The time lets the UI see an
// app restart, which otherwise looks identical to no change. Anything unreadable
// means "" -- not serving, the safe default (the app is not up).
func (m *Manager) appProcessState(name string) (state string, startedAt int64) {
	root, err := m.homefs.OpenRoot(m.appFiles(name))
	if err != nil {
		return "", 0
	}
	defer root.Close()
	b, err := m.homefs.ReadCapped(root, appStateFile, maxStateRead)
	if err != nil {
		return "", 0
	}
	// Milliseconds, not seconds: a restart of an app that started less than a second
	// ago would otherwise land in the same second and look like nothing changed.
	if stat, err := root.Stat(appStateFile); err == nil {
		startedAt = stat.ModTime().UnixMilli()
	}
	return strings.TrimSpace(string(b)), startedAt
}

// appStateFor is the app process state the UI shows. A down container has no app
// process, so it is blank regardless of a stale breadcrumb; otherwise it is the
// agent's breadcrumb verbatim.
func appStateFor(containerRunning bool, breadcrumb string) string {
	if !containerRunning {
		return ""
	}
	return breadcrumb
}

// containerStartTimes reports when each app's container last started, as Unix
// seconds; a container that is not running is simply absent. podman prints one
// line per running container, which recreate/restart makes newer -- the signal
// the UI uses to know a reboot actually happened.
func (m *Manager) containerStartTimes(names []string) map[string]int64 {
	starts := make(map[string]int64, len(names))
	out, err := m.container.RunningStartTimes(stateTimeout)
	if err != nil {
		return starts
	}
	nameByID := m.nameByID() // containers are id-named; map back to the app name callers key on
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(fields) != 2 {
			continue
		}
		id := strings.TrimPrefix(fields[0], workspace.ContainerPrefix)
		if id == fields[0] {
			continue // Not one of ours
		}
		name, ok := nameByID[id]
		if !ok {
			continue
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
	units := make([]string, len(names))
	for i, name := range names {
		units[i] = m.unitName(name)
	}
	out, _ := m.systemd.IsActive(stateTimeout, units...) // Non-zero exit just means "something is inactive"
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
	out, err := m.container.Stats(stateTimeout)
	if err != nil {
		return usages
	}
	stats, err := container.ParseStats(out)
	if err != nil {
		return usages
	}
	nameByID := m.nameByID() // containers are id-named; map back to the app name
	for _, stat := range stats {
		id := strings.TrimPrefix(stat.Name, workspace.ContainerPrefix)
		if id == stat.Name {
			continue // Not one of ours
		}
		name, ok := nameByID[id]
		if !ok {
			continue
		}
		usages[name] = usage{memoryMB: stat.MemoryMB, cpuPercent: stat.CPUPercent}
	}
	return usages
}

// nameByID maps every app's id to its current name, for turning id-keyed host
// state (container names) back into the names callers use.
func (m *Manager) nameByID() map[string]string {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps to map ids to names", "error", err)
		return map[string]string{}
	}
	byID := make(map[string]string, len(apps))
	for _, a := range apps {
		byID[a.ID] = a.Name
	}
	return byID
}
