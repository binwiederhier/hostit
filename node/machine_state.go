package node

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/stats"
	"heckel.io/hostit/workspace"
)

const (
	// stateTimeout bounds the commands behind a state refresh. podman serializes
	// on its own lock, so a create or pull in flight can take minutes.
	stateTimeout = 2 * time.Second
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

// appStateFor is the app process state the UI shows. A down container has no app
// process, so it is blank regardless of a stale breadcrumb; otherwise it is the
// agent's breadcrumb verbatim.
func appStateFor(containerRunning bool, breadcrumb string) string {
	if !containerRunning {
		return ""
	}
	return breadcrumb
}

// SeedStates pre-fills the cache from recorded intent, before the first real
// measurement: an app is presumed running unless its poweroff was recorded.
// Serving starts before the first podman/systemd round trip completes, and an
// empty cache would report every app as stopped for a moment after a daemon
// restart (a red status dot on the first page load). Real numbers replace the
// seed as soon as the first refresh lands; existing entries are never touched.
func (m *Machine) SeedStates() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps to seed states", "error", err)
		return
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	for _, a := range apps {
		if _, ok := m.stateCache[a.Name]; ok {
			continue
		}
		state := api.State{}
		if !a.PoweredOff {
			state = api.State{Running: true, AppRunning: true, AppState: app.StateRunning}
		}
		m.stateCache[a.Name] = state
	}
}

// stateChanged is called when an app was just started, stopped or replaced. It
// drops what the cache knew, and looks again shortly after.
//
// Forgetting alone is not enough: "systemctl start" returns before the unit
// reports active, so an immediate measurement records "stopped" and the cache
// then serves that confidently for a whole TTL. The owner is watching the status
// dot while this happens, so the answer has to catch up in seconds, not tens of
// seconds.
func (m *Machine) stateChanged(name string) {
	m.stateMu.Lock()
	delete(m.stateCache, name)
	m.stateMu.Unlock()
	// The other half's cache would otherwise serve the pre-transition state
	// for a whole TTL; the hook invalidates its entry (see NewManager).
	if m.onStateChanged != nil {
		m.onStateChanged(name)
	}
	// Re-measure repeatedly for the length of a transition, not just once: the UI
	// watches the app's start time to know a reboot/restart finished, and it can
	// only see that if the cache is refreshed the whole time the action is settling.
	go func() {
		for elapsed := time.Duration(0); elapsed < stateSettleWindow; elapsed += stateSettleInterval {
			time.Sleep(stateSettleInterval)
			m.refreshLocal()
		}
	}()
}

// beginRefresh claims the right to refresh, so only one runs at a time. Two
// concurrent refreshes would ask podman twice and, worse, the slower one would
// write last -- stamping older numbers with a newer freshness time.
func (m *Machine) beginRefresh() bool {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.stateRefreshing {
		return false
	}
	m.stateRefreshing = true
	return true
}

func (m *Machine) doneRefreshing() {
	m.stateMu.Lock()
	m.stateRefreshing = false
	m.stateMu.Unlock()
}

// refreshLocal is the Machine half's own refresh: measure every app in this
// half's store with the LOCAL machinery and swap the cache. The control
// plane's RefreshStates instead reads through the node agent; in a single
// process the two are the same measurement.
func (m *Machine) refreshLocal() {
	if !m.beginRefresh() {
		return
	}
	defer m.doneRefreshing()
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
func (m *Machine) StateLoop(done <-chan struct{}) {
	slog.Info("Starting app state loop", "interval", stateRefreshInterval)
	defer slog.Info("Stopping app state loop")
	m.refreshLocal() // Prime it, so the first page load already has numbers
	for {
		select {
		case <-time.After(stateRefreshInterval):
		case <-done:
			return
		}
		m.refreshLocal()
	}
}

// States measures the given apps. Both podman and systemd are asked once for
// all of them rather than once per app, so the cost does not grow with the
// number of apps.
func (m *Machine) States(names []string) map[string]api.State {
	states := make(map[string]api.State, len(names))
	if len(names) == 0 {
		return states
	}
	starts := m.containerStartTimes(names)
	for name, running := range m.runningStates(names) {
		// The app can only be up if its container is; a stopped or crashed app
		// leaves the container running but reports something other than "running".
		appState, appStartedAt := m.AppProcessState(name)
		states[name] = api.State{
			Running:      running,
			AppRunning:   running && appState == app.StateRunning,
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
func (m *Machine) AppProcessState(name string) (state string, startedAt int64) {
	root, err := m.homefs.OpenRoot(m.AppFiles(name))
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

// containerStartTimes reports when each app's container last started, as Unix
// seconds; a container that is not running is simply absent. podman prints one
// line per running container, which recreate/restart makes newer -- the signal
// the UI uses to know a reboot actually happened.
func (m *Machine) containerStartTimes(names []string) map[string]int64 {
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
func (m *Machine) runningStates(names []string) map[string]bool {
	units := make([]string, len(names))
	for i, name := range names {
		units[i] = m.UnitName(name)
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
func (m *Machine) resourceUsage() map[string]usage {
	usages := make(map[string]usage)
	out, err := m.container.Stats(stateTimeout)
	if err != nil {
		return usages
	}
	stats, err := podman.ParseStats(out)
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
func (m *Machine) nameByID() map[string]string {
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

// api.Heartbeat reports this node's build and capabilities: the placement and
// health inputs of the multi-node design. Grown in later phases (free
// memory/disk, app count).
func (m *Machine) Heartbeat() *api.Heartbeat {
	return &api.Heartbeat{
		Version:      Version,
		BtrfsCapable: m.btrfs.IsBtrfs(m.config.AppsDir),
		Address:      m.config.AppsBindAddress,
		SSHHost:      m.config.SSHHost,
		SSHHostKey:   m.sshHostKey(),
		// The apps pool is the filesystem that matters here: it filling up is
		// what breaks this node.
		Stats: stats.Measure(m.config.AppsDir),
	}
}

// sshHostKeyFile is the node's sshd ed25519 public host key, at the standard
// location every distro's sshd generates. A package var so a test can point it
// elsewhere; not a config option -- no deployment has ever needed to override it.
var sshHostKeyFile = "/etc/ssh/ssh_host_ed25519_key.pub"

// sshHostKey reads this node's sshd public host key (one line), for the relay
// gateway's known_hosts. Empty if unreadable -- control then keeps its last
// value and the node simply has no relay entry until it reports one.
func (m *Machine) sshHostKey() string {
	data, err := os.ReadFile(sshHostKeyFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
