package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

func TestNodeRegistryRegisterSupersedeUnregister(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	reg := NewNodeRegistry()
	first := &recordingAgent{NodeAgent: m}
	second := &recordingAgent{NodeAgent: m}

	reg.Register("local", first)
	assert.Equal(t, NodeAgent(first), reg.Agent("local"))

	// A redial supersedes; unregistering the STALE agent is a no-op.
	reg.Register("local", second)
	reg.Unregister("local", first)
	assert.Equal(t, NodeAgent(second), reg.Agent("local"))

	reg.Unregister("local", second)
	assert.Nil(t, reg.Agent("local"))
	assert.Empty(t, reg.IDs())
}

func TestPlacementPicksTheEmptiestConnectedNode(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	reg := NewNodeRegistry()
	m.SetNodeRegistry(reg)
	// Two apps live on "local", none on "worker-2"; both nodes are connected.
	require.NoError(t, m.store.AddApp(&store.App{ID: "a1", Name: "one", Port: 10001, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{ID: "a2", Name: "two", Port: 10002, Host: store.HostLocal}))
	reg.Register("local", &recordingAgent{NodeAgent: m})
	reg.Register("worker-2", &recordingAgent{NodeAgent: m})

	assert.Equal(t, "worker-2", m.placeNode())

	// Only local connected: everything lands there, however full.
	reg.Unregister("worker-2", reg.Agent("worker-2"))
	assert.Equal(t, "local", m.placeNode())
}

func TestRoutingAgentRoutesByAppHost(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	reg := NewNodeRegistry()
	require.NoError(t, m.store.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10001, Host: "worker-2"}))
	local := &recordingAgent{NodeAgent: m}
	remote := &recordingAgent{NodeAgent: m}
	reg.Register("local", local)
	reg.Register("worker-2", remote)
	routing := NewRoutingAgent(m.store, reg)

	// A name-keyed verb goes to the app's host.
	_, err := routing.Status("blog")
	require.NoError(t, err)

	// Spec-keyed verbs go to the spec's host.
	require.Error(t, routing.Provision(&ProvisionSpec{Name: "x", Host: "ghost"}), "an unconnected host is an error, not a silent local fallback")

	// A disconnected node's verb fails loudly.
	reg.Unregister("worker-2", remote)
	_, err = routing.Status("blog")
	require.Error(t, err)
}

func TestIngestStatesMergesPerName(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	m.IngestStates(map[string]State{"one": {AppState: "running"}})
	m.IngestStates(map[string]State{"two": {AppState: "stopped"}})
	states := m.CachedStates([]string{"one", "two"})
	// Per-node poll loops must not clobber each other's apps.
	assert.Equal(t, "running", states["one"].AppState)
	assert.Equal(t, "stopped", states["two"].AppState)
}

func TestMachineLifecycleInvalidatesTheControlCache(t *testing.T) {
	t.Parallel()
	// The control plane serves states from its own cache; a lifecycle change
	// the machine half performs (Down here) must invalidate that entry so the
	// UI does not confidently serve the old state for a whole TTL. Guarded
	// across the cache split: with one shared map this held trivially, with
	// two it needs the machine's invalidation hook.
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	m.IngestStates(map[string]State{"blog": {Running: true, AppRunning: true, AppState: "running"}})

	require.NoError(t, m.Down("blog"))

	_, machineHas := m.MeasuredState("blog")
	assert.False(t, machineHas, "the machine's own cache entry is dropped")
	cached := m.CachedStates([]string{"blog"})
	assert.False(t, cached["blog"].Running, "the control cache must not keep serving the pre-Down state")
}

// A node must only ever be handed the apps it hosts. A fleet-wide document
// fanned out unsliced would have every node try to build and configure apps
// that live elsewhere -- and on a colocated pair, which shares /etc/passwd,
// the app would look present and quietly take another node's limits.
func TestReconcileSlicesTheDocumentPerNode(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	reg := NewNodeRegistry()
	local := &desiredRecorder{NodeAgent: m}
	worker := &desiredRecorder{NodeAgent: m}
	reg.Register(store.HostLocal, local)
	reg.Register("worker-2", worker)

	NewRoutingAgent(m.Store(), reg).Reconcile(&nodeapi.DesiredState{Apps: []*nodeapi.AppDesired{
		{ProvisionSpec: nodeapi.ProvisionSpec{Name: "here", Host: store.HostLocal}},
		{ProvisionSpec: nodeapi.ProvisionSpec{Name: "there", Host: "worker-2"}},
	}})

	assert.Equal(t, []string{"here"}, local.names())
	assert.Equal(t, []string{"there"}, worker.names())
}

type desiredRecorder struct {
	NodeAgent
	got *nodeapi.DesiredState
}

func (r *desiredRecorder) Reconcile(desired *nodeapi.DesiredState) []string {
	r.got = desired
	return nil
}

func (r *desiredRecorder) names() []string {
	var out []string
	for _, a := range r.got.Apps {
		out = append(out, a.Name)
	}
	return out
}
