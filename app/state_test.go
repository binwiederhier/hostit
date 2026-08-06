package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMemMB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"12.3MB / 512MB", 12},
		{"1.5GB / 2GB", 1536},
		{"800kB / 512MB", 0},
		{"512B", 0},
		{"", 0},
		{"garbage", 0},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseMemMB(tt.in), "input %q", tt.in)
	}
}

func TestStatesReadsRunningAndMemory(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	createTestApp(t, m, "two")
	// systemctl prints one line per unit, in the order asked
	runner.returns("systemctl is-active", "active\ninactive\n")
	// Exactly the shape podman 4.9 prints
	runner.returns("podman stats", `[{"id":"abc","name":"hostit-app-one","cpu_percent":"0.00%","mem_usage":"24.5MB / 536.9MB","pids":"11"},
	                                 {"id":"def","name":"not-ours","mem_usage":"99MB / 536.9MB"}]`)
	states := m.States([]string{"one", "two"})
	require.Len(t, states, 2)
	assert.True(t, states["one"].Running)
	assert.False(t, states["two"].Running, "the second unit was inactive")
	assert.Equal(t, 24, states["one"].MemoryMB)
	assert.Equal(t, 0, states["two"].MemoryMB, "no stats line means no usage")
}

func TestStatesReportsAppProcessState(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "up")     // container up, agent says the app is running
	createTestApp(t, m, "paused") // container up, but the app was stopped
	createTestApp(t, m, "fresh")  // container up, but the agent left no breadcrumb yet
	createTestApp(t, m, "off")    // container down
	writeAppFile(t, m, "up", "log/state", "running\n")
	writeAppFile(t, m, "paused", "log/state", "stopped\n")
	// "fresh" has no log/state file at all
	runner.returns("systemctl is-active", "active\nactive\nactive\ninactive\n")

	states := m.States([]string{"up", "paused", "fresh", "off"})
	require.Len(t, states, 4)
	assert.True(t, states["up"].AppRunning, "the agent reported the app running")
	assert.False(t, states["paused"].AppRunning, "the agent reported the app stopped")
	assert.False(t, states["fresh"].AppRunning, "no breadcrumb means not serving")
	assert.False(t, states["off"].AppRunning, "a down container cannot be serving")
	// The container is up for all but "off", regardless of the app process
	assert.True(t, states["paused"].Running)
	assert.False(t, states["off"].Running)
}

func TestStatesReportsStartTimes(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("systemctl is-active", "active\n")
	runner.returns("podman ps", "hostit-app-one|1786001000\nnot-ours|1786000000\n")
	writeAppFile(t, m, "one", "log/state", "running\n")

	// The container start time comes from podman; the app start time is the state
	// file's mtime. Pin the mtime so the assertion is exact.
	stateFile := filepath.Join(m.appHome("one"), "log", "state")
	early := time.Unix(1786001111, 0)
	require.NoError(t, os.Chtimes(stateFile, early, early))

	states := m.States([]string{"one"})
	assert.Equal(t, int64(1786001000), states["one"].StartedAt, "container start time from podman ps")
	assert.Equal(t, early.UnixMilli(), states["one"].AppStartedAt, "app start time from the state file mtime")

	// A restart rewrites the state file, bumping its mtime -- this is the only
	// signal that tells "the app restarted" apart from "nothing happened", since
	// the running state is identical before and after.
	later := time.Unix(1786009999, 0)
	require.NoError(t, os.Chtimes(stateFile, later, later))
	states = m.States([]string{"one"})
	assert.Equal(t, later.UnixMilli(), states["one"].AppStartedAt, "the restart moved the app start time forward")
	assert.Greater(t, states["one"].AppStartedAt, early.UnixMilli())
}

func TestStatesDegradeRatherThanBlock(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	// podman is busy (a create or pull holds its lock), so the bounded call
	// fails; the listing must still answer, just without memory numbers
	runner.failOn("podman stats", assert.AnError)
	runner.returns("systemctl is-active", "active\n")
	states := m.States([]string{"one"})
	require.Len(t, states, 1)
	assert.True(t, states["one"].Running, "systemd still answers")
	assert.Equal(t, 0, states["one"].MemoryMB)
}

func TestCachedStatesAnswerFromMemory(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("systemctl is-active", "active\n")
	runner.returns("podman stats", `[{"name":"hostit-app-one","mem_usage":"7MB / 536.9MB"}]`)
	m.RefreshStates()
	runner.reset()

	// A read is served from the cache: no podman, no systemd, no waiting
	states := m.CachedStates([]string{"one"})
	assert.True(t, states["one"].Running)
	assert.Equal(t, 7, states["one"].MemoryMB)
	assert.NotContains(t, runner.ran(), "podman", "the request path must not shell out")
	assert.NotContains(t, runner.ran(), "systemctl")
}

func TestCachedStatesRefreshInBackgroundWhenStale(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("systemctl is-active", "active\n")
	runner.returns("podman stats", `[{"name":"hostit-app-one","mem_usage":"9MB / 536.9MB"}]`)

	// Nothing measured yet: the first read returns zeroes rather than blocking,
	// and triggers a refresh that lands shortly after
	states := m.CachedStates([]string{"one"})
	assert.False(t, states["one"].Running)
	require.Eventually(t, func() bool {
		return m.CachedStates([]string{"one"})["one"].MemoryMB == 9
	}, 5*time.Second, 10*time.Millisecond, "the background refresh must fill the cache")
}

func TestStatesWithNoApps(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	assert.Empty(t, m.States(nil))
}

func TestStatesSurvivesBrokenOutput(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("podman stats", "not json at all")
	states := m.States([]string{"one"})
	// A broken stats call must not lose the running state or crash the listing
	require.Len(t, states, 1)
	assert.Equal(t, 0, states["one"].MemoryMB)
}

func TestCachedStatesRefreshForAnAppItHasNeverSeen(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("systemctl is-active", "active\n")
	runner.returns("podman stats", `[{"name":"hostit-app-one","mem_usage":"5MB / 536.9MB"}]`)
	m.RefreshStates()

	// A just-created app is the case that matters: its owner is redirected to its
	// page immediately and sees the status dot. Waiting for the TTL to lapse
	// would show "stopped" for ten seconds after it started.
	createTestApp(t, m, "two")
	runner.returns("systemctl is-active", "active\nactive\n")
	runner.returns("podman stats", `[{"name":"hostit-app-one","mem_usage":"5MB / 536.9MB"},{"name":"hostit-app-two","mem_usage":"3MB / 536.9MB"}]`)
	states := m.CachedStates([]string{"two"})
	assert.False(t, states["two"].Running, "the answer is still immediate")
	require.Eventually(t, func() bool {
		return m.CachedStates([]string{"two"})["two"].Running
	}, 5*time.Second, 10*time.Millisecond, "an unknown app must trigger a refresh, TTL or not")
}

func TestLifecycleInvalidatesTheCachedState(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("systemctl is-active", "inactive\n")
	m.RefreshStates()
	require.False(t, m.CachedStates([]string{"one"})["one"].Running)

	// Someone just pressed Start. Answering "stopped" for another ten seconds
	// makes the button look broken.
	runner.returns("systemctl is-active", "active\n")
	_, err := m.Ensure("one") // What POST /start calls
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return m.CachedStates([]string{"one"})["one"].Running
	}, 5*time.Second, 10*time.Millisecond, "starting an app must make its cached state stale")

	// Every way to change an app's state has to do this, or the dot lies
	runner.returns("systemctl is-active", "inactive\n")
	require.NoError(t, m.Down("one"))
	require.Eventually(t, func() bool {
		return !m.CachedStates([]string{"one"})["one"].Running
	}, 5*time.Second, 10*time.Millisecond, "stopping an app must too")
}
