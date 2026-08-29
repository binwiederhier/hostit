package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/stats"
)

func TestScopeStatesDropsAppsNotAskedAbout(t *testing.T) {
	t.Parallel()
	// A node may only report states for the apps control polled it about (the
	// apps it hosts). A compromised node returning an extra key for another
	// node's app must not reach the state cache.
	asked := []string{"mine1", "mine2"}
	reported := map[string]control.State{
		"mine1":  {AppState: "running"},
		"mine2":  {AppState: "stopped"},
		"victim": {AppState: "running"}, // an app on another node
	}
	scoped := scopeStates(reported, asked)
	assert.Len(t, scoped, 2)
	assert.Contains(t, scoped, "mine1")
	assert.Contains(t, scoped, "mine2")
	assert.NotContains(t, scoped, "victim", "a node cannot report state for an app it was not asked about")
}

// fakeNodeAgent overrides just what pollNodeOnce touches; calling anything
// else hits the embedded nil interface and panics the test, which is the point.
type fakeNodeAgent struct {
	control.NodeAgent
	statesCalls    int
	heartbeatCalls int
	stats          stats.Stats
}

func (f *fakeNodeAgent) States(names []string) map[string]api.State {
	f.statesCalls++
	return map[string]api.State{}
}

func (f *fakeNodeAgent) Heartbeat() *api.Heartbeat {
	f.heartbeatCalls++
	return &api.Heartbeat{Address: "10.0.0.4", Stats: f.stats}
}

// A node hosting nothing must still register a pulse: this poll doubles as
// the liveness heartbeat, and skipping empty nodes froze their LAST SEEN at
// the moment their last app left (and made a freshly added node read as dead
// before its first app ever arrived). Found live on stage-2, 2026-08-20.
func TestPollStampsLivenessOfAnEmptyNode(t *testing.T) {
	t.Parallel()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	manager := control.NewManager(config.NewConfig(), s)
	require.NoError(t, s.EnsureNode("stage-2", "10.0.0.4"))

	agent := &fakeNodeAgent{}
	pollNodeOnce(manager, "stage-2", agent, true)

	n, err := s.Node("stage-2")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), n.LastSeen, time.Minute, "an empty node's pulse still stamps last_seen")
}

// A node's machine stats have to be REFRESHED, not captured once. They used to
// be written only by the connect handshake, so a node that stayed connected
// showed whatever its load happened to be at the moment it dialled in --
// forever. Found on prod straight after a deploy: load1 frozen at 15.92 while
// the machine was actually sitting at 1.52, with last_seen ticking along
// happily beside it.
func TestPollRefreshesMachineStats(t *testing.T) {
	t.Parallel()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	manager := control.NewManager(config.NewConfig(), s)
	require.NoError(t, s.EnsureNode("node-1", "10.0.0.4"))

	agent := &fakeNodeAgent{stats: stats.Stats{Load1: 15.92, MemoryUsedMB: 641}}
	pollNodeOnce(manager, "node-1", agent, true)
	n, err := s.Node("node-1")
	require.NoError(t, err)
	assert.Contains(t, n.Stats, "15.92", "the first poll records what the node reported")

	// The machine calms down; the next stats poll must say so.
	agent.stats = stats.Stats{Load1: 1.52, MemoryUsedMB: 640}
	pollNodeOnce(manager, "node-1", agent, true)
	n, err = s.Node("node-1")
	require.NoError(t, err)
	assert.Contains(t, n.Stats, "1.52", "and a later one replaces it")
	assert.NotContains(t, n.Stats, "15.92")
}

// Stats ride a slower cadence than the state poll: the poll runs every couple
// of seconds to keep app state warm, and telemetry for a dashboard does not
// need asking that often.
func TestTheStatePollDoesNotHeartbeatEveryTick(t *testing.T) {
	t.Parallel()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	manager := control.NewManager(config.NewConfig(), s)
	require.NoError(t, s.EnsureNode("node-1", "10.0.0.4"))
	require.NoError(t, s.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: "node-1"}))

	agent := &fakeNodeAgent{}
	pollNodeOnce(manager, "node-1", agent, false)
	assert.Zero(t, agent.heartbeatCalls, "an ordinary tick does not ask for stats")
	assert.Equal(t, 1, agent.statesCalls, "it still measures the node's apps")

	pollNodeOnce(manager, "node-1", agent, true)
	assert.Equal(t, 1, agent.heartbeatCalls, "the stats tick does")
}
