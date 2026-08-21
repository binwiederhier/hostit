package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
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
}

func (f *fakeNodeAgent) States(names []string) map[string]nodeapi.State {
	f.statesCalls++
	return map[string]nodeapi.State{}
}

func (f *fakeNodeAgent) Heartbeat() *nodeapi.Heartbeat {
	f.heartbeatCalls++
	return &nodeapi.Heartbeat{}
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
	manager := control.NewManager(controlconf.NewConfig(), s)
	require.NoError(t, s.EnsureNode("stage-2", "10.0.0.4"))

	agent := &fakeNodeAgent{}
	pollNodeOnce(manager, "stage-2", agent)

	n, err := s.Node("stage-2")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), n.LastSeen, time.Minute, "an empty node's pulse still stamps last_seen")
}
