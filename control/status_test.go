package control

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// One read of the registry answers the whole question an operator asks: which
// members are there, are they reporting, and what is the cluster carrying.
func TestClusterStatusReportsMembersAndTotals(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	st := s.apps.Store()
	now := time.Now()

	require.NoError(t, st.EnsureNode("worker-1", "10.0.0.2"))
	require.NoError(t, st.SetNodeSeen("worker-1", now.Add(-5*time.Second)))
	require.NoError(t, st.EnsureNode("worker-2", "10.0.0.3")) // never reported
	require.NoError(t, st.EnsureProxy("edge-1"))
	require.NoError(t, st.SetProxyStatus("edge-1", now.Add(-10*time.Second), "v0.13.0", 4))

	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: "worker-1"}))
	require.NoError(t, st.AddApp(&store.App{ID: "a2", Name: "wiki", Port: 10001, Host: "worker-1"}))
	// Usage is measured by the daemon and written to the registry, which is what
	// makes it readable from another process.
	require.NoError(t, st.UpdateAppUsage("blog", 120))
	require.NoError(t, st.UpdateAppUsage("wiki", 80))
	require.NoError(t, st.AddSnapshot(&store.Snapshot{ID: "s1", AppName: "blog", CreatedAt: now}))
	require.NoError(t, st.AddApp(&store.App{ID: "a3", Name: "gone", Port: 10002, Host: "retired-node"}))
	require.NoError(t, st.SetAppPoweredOff("wiki", true))

	status, err := ClusterStatus(st, now)
	require.NoError(t, err)

	require.Len(t, status.Nodes, 2)
	assert.Equal(t, "worker-1", status.Nodes[0].Name)
	assert.Equal(t, 2, status.Nodes[0].Apps, "a node's row says what it carries")
	assert.False(t, status.Nodes[0].Stale, "a node that reported seconds ago is not stale")
	assert.True(t, status.Nodes[1].Stale, "a node that never reported is stale")

	require.Len(t, status.Proxies, 1)
	assert.Equal(t, "v0.13.0", status.Proxies[0].Version)
	assert.Equal(t, 4, status.Proxies[0].Routes)
	assert.False(t, status.Proxies[0].Stale)

	assert.Equal(t, 3, status.Apps.Total)
	assert.Equal(t, 1, status.Apps.PoweredOff)
	assert.Equal(t, 200, status.Apps.DiskUsedMB)
	assert.Equal(t, 1, status.Apps.Snapshots)
	assert.Equal(t, 1, status.Apps.Unplaced, "an app whose node is not registered is worth flagging")
}

// A member that stopped reporting is the thing an operator is looking for, so
// the staleness line has to be drawn where a slow pass cannot cross it.
func TestClusterStatusFlagsAMemberThatStoppedReporting(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	st := s.apps.Store()
	now := time.Now()
	require.NoError(t, st.EnsureProxy("edge-1"))
	require.NoError(t, st.SetProxyStatus("edge-1", now.Add(-90*time.Second), "v1", 3))
	require.NoError(t, st.EnsureProxy("edge-2"))
	require.NoError(t, st.SetProxyStatus("edge-2", now.Add(-10*time.Minute), "v1", 3))

	status, err := ClusterStatus(st, now)
	require.NoError(t, err)
	require.Len(t, status.Proxies, 2)
	assert.False(t, status.Proxies[0].Stale, "90s is a slow pass, not a dead proxy")
	assert.True(t, status.Proxies[1].Stale, "ten minutes of silence is not a slow pass")
}

// When control does the machine work itself, it IS the node -- and it has to
// say so. Nothing registered the colocated node in that shape, so the registry
// held apps whose host matched no node: `node list` was empty, and the cluster
// view told an operator that every app was on an unregistered node and "not
// routable", about apps it was serving at that moment (prod, 2026-08-17).
func TestTheColocatedNodeRegistersItself(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal}))

	require.NoError(t, s.apps.EnsureLocalNode())

	status, err := ClusterStatus(st, time.Now())
	require.NoError(t, err)
	require.Len(t, status.Nodes, 1)
	assert.Equal(t, store.HostLocal, status.Nodes[0].Name)
	assert.Equal(t, 1, status.Nodes[0].Apps)
	assert.False(t, status.Nodes[0].Stale, "control's own liveness is the colocated node's")
	assert.Equal(t, 0, status.Apps.Unplaced, "an app on the local node is placed")
}

// An app whose host really is missing still gets flagged -- that is the case
// the count exists for.
func TestClusterStatusStillFlagsATrulyUnplacedApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	st := s.apps.Store()
	require.NoError(t, s.apps.EnsureLocalNode())
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "orphan", Port: 10000, Host: "retired-node"}))

	status, err := ClusterStatus(st, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, status.Apps.Unplaced)
}
