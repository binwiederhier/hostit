package control

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// recordingAgent wraps the manager's own NodeAgent implementation, recording
// Sync pushes -- the shape of a remote node from the control plane's view.
type recordingAgent struct {
	NodeAgent
	syncs []*SyncState
}

func (r *recordingAgent) Sync(state *SyncState) error {
	r.syncs = append(r.syncs, state)
	return nil
}

func TestCreatePushesMirrorToTheNode(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	rec := &recordingAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, rec)

	_, err := m.CreateApp("blog", &CreateOptions{})
	require.NoError(t, err)
	m.WaitBackground()

	// The mirror push happens after the registry row exists (the node's Up and
	// SetDiskLimit read the row on the node side).
	require.NotEmpty(t, rec.syncs)
	last := rec.syncs[len(rec.syncs)-1]
	names := make([]string, 0)
	for _, a := range last.Apps {
		names = append(names, a.Name)
	}
	assert.Contains(t, names, "blog")
}

func TestDeletePushesMirrorWithoutTheApp(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	rec := &recordingAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, rec)

	require.NoError(t, m.DeleteApp("blog"))
	m.WaitBackground()

	require.NotEmpty(t, rec.syncs)
	last := rec.syncs[len(rec.syncs)-1]
	for _, a := range last.Apps {
		assert.NotEqual(t, "blog", a.Name)
	}
}

func TestFusedManagerDoesNotSyncItself(t *testing.T) {
	t.Parallel()
	// Single process: the store IS the registry; a mirror push would replace
	// the registry with itself (and clobber concurrent writes).
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog") // must not panic or wipe anything
	apps, err := m.store.Apps()
	require.NoError(t, err)
	assert.Len(t, apps, 1)
}

// fakeSink records the node-originated control-plane callbacks.
type fakeSink struct {
	power     map[string]bool
	usage     map[string]int
	snapshots map[string][]*store.Snapshot
}

func newFakeSink() *fakeSink {
	return &fakeSink{power: map[string]bool{}, usage: map[string]int{}, snapshots: map[string][]*store.Snapshot{}}
}

func (f *fakeSink) PowerChanged(name string, off bool)                    { f.power[name] = off }
func (f *fakeSink) UsageChanged(name string, usedMB int)                  { f.usage[name] = usedMB }
func (f *fakeSink) SnapshotsChanged(name string, snaps []*store.Snapshot) { f.snapshots[name] = snaps }

func TestPowerOffNotifiesTheSink(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	sink := newFakeSink()
	m.testMachine().SetControlSink(sink)

	require.NoError(t, m.testMachine().Down("blog"))
	off, ok := sink.power["blog"]
	require.True(t, ok, "a poweroff the node performs must reach control")
	assert.True(t, off)

	_, err := m.testMachine().PowerOn("blog")
	require.NoError(t, err)
	assert.False(t, sink.power["blog"])
}

func TestSnapshotMutationsNotifyTheSink(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "300\n")
	createTestApp(t, m, "blog")
	sink := newFakeSink()
	m.testMachine().SetControlSink(sink)

	snap, err := m.testMachine().TakeSnapshot("blog", "save", false)
	require.NoError(t, err)
	ids := func() []string {
		out := make([]string, 0)
		for _, rec := range sink.snapshots["blog"] {
			out = append(out, rec.ID)
		}
		return out
	}
	assert.Contains(t, ids(), snap.ID, "the record list travels with the callback")

	require.NoError(t, m.testMachine().DeleteSnapshot("blog", snap.ID))
	assert.NotContains(t, ids(), snap.ID)
}

func TestCreatePushesMirrorBeforeProvision(t *testing.T) {
	t.Parallel()
	// The node's reconcile treats unknown ids as orphans: if machine state
	// (subvolume, user) appears before the mirror knows the id, a concurrent
	// reconcile on the node tears the half-built app down (seen live on the
	// stage two-node setup). The row and the mirror push must come FIRST.
	m, _, _ := newTestDeployManager(t)
	rec := &orderRecordingAgent{recordingAgent: recordingAgent{NodeAgent: m.testMachine()}}
	m.NodeRegistry().Register(store.HostLocal, rec)

	_, err := m.CreateApp("blog", &CreateOptions{})
	require.NoError(t, err)
	m.WaitBackground()
	require.True(t, rec.syncBeforeProvision, "the mirror must know the app id before any machine state exists")
}

// orderRecordingAgent flags whether a Sync carrying the app arrived before
// Provision ran.
type orderRecordingAgent struct {
	recordingAgent
	syncBeforeProvision bool
}

func (r *orderRecordingAgent) Provision(spec *ProvisionSpec) error {
	for _, s := range r.syncs {
		for _, a := range s.Apps {
			if a.ID == spec.ID {
				r.syncBeforeProvision = true
			}
		}
	}
	return r.recordingAgent.NodeAgent.Provision(spec)
}

func (r *orderRecordingAgent) Sync(state *SyncState) error {
	return r.recordingAgent.Sync(state)
}

// fileRoutingAgent records file verbs, standing in for a remote node.
type fileRoutingAgent struct {
	NodeAgent
	reads, writes []string
}

func (f *fileRoutingAgent) ReadFile(name, rel string) ([]byte, error) {
	f.reads = append(f.reads, name+":"+rel)
	return f.NodeAgent.ReadFile(name, rel)
}

func (f *fileRoutingAgent) ReadFileMax(name, rel string, max int64) ([]byte, error) {
	f.reads = append(f.reads, name+":"+rel)
	return f.NodeAgent.ReadFileMax(name, rel, max)
}

func (f *fileRoutingAgent) WriteFile(name, rel string, b []byte, mode os.FileMode) error {
	f.writes = append(f.writes, name+":"+rel)
	return f.NodeAgent.WriteFile(name, rel, b, mode)
}

func TestReadmeAndDescriptionRouteThroughTheNodeAgent(t *testing.T) {
	t.Parallel()
	// Readme/Description are control-plane COMPOSITIONS, but their file reads
	// and writes must go through the node agent: the file lives on the app's
	// hosting node, not on control's machine (a node2 app's readme PUT hit
	// control's local raw view and 500ed, live on stage).
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	rec := &fileRoutingAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, rec)

	require.NoError(t, m.WriteReadme("blog", "hello"))
	_, err := m.Readme("blog")
	require.NoError(t, err)
	m.Description("blog")
	require.NoError(t, m.SetDescription("blog", "an app"))

	assert.NotEmpty(t, rec.writes, "readme/description writes go to the node")
	assert.NotEmpty(t, rec.reads, "readme/description reads come from the node")
}

// statesRoutingAgent reports canned states, standing in for the fan-out to
// remote nodes.
type statesRoutingAgent struct {
	NodeAgent
	states map[string]State
}

func (s *statesRoutingAgent) States(names []string) map[string]State {
	out := make(map[string]State, len(names))
	for _, n := range names {
		if st, ok := s.states[n]; ok {
			out[n] = st
		}
	}
	return out
}

func TestRefreshStatesReadsThroughTheNodeAgent(t *testing.T) {
	t.Parallel()
	// In split mode control has no local app containers: measuring locally and
	// whole-swapping the cache would clobber the per-node poll data with empty
	// "stopped" states. RefreshStates must read through the node agent (which
	// fans out to the nodes), not control's own podman/systemd.
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	m.SetNodeAgent(&statesRoutingAgent{NodeAgent: m.testMachine(), states: map[string]State{
		"blog": {Running: true, AppRunning: true, AppState: "running"},
	}})

	m.RefreshStates()
	got := m.CachedStates([]string{"blog"})
	assert.Equal(t, "running", got["blog"].AppState, "state comes from the node agent, not a local measure")
	assert.True(t, got["blog"].Running)
}

// Each mirror push carries a higher sequence than the one before it, so a
// node can tell a newer payload from an older one that raced it (the node's
// half of this is TestSyncIgnoresAStaleMirror).
func TestMirrorPushesCarryIncreasingSequences(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	rec := &recordingAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, rec)

	_, err := m.CreateApp("one", nil)
	require.NoError(t, err)
	_, err = m.CreateApp("two", nil)
	require.NoError(t, err)
	m.WaitBackground()

	require.GreaterOrEqual(t, len(rec.syncs), 2)
	for i := 1; i < len(rec.syncs); i++ {
		assert.Greater(t, rec.syncs[i].Seq, rec.syncs[i-1].Seq, "sequences must increase with each push")
	}
}

// Seq order must be READ order: a payload built from a later registry state
// must carry the higher number. Stamping after the read let two builders
// interleave the other way round, and the node -- which keeps only the
// highest -- then held the older app set permanently (an app created in that
// window never started, seen twice on stage).
func TestMirrorSequenceFollowsRegistryOrder(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int64]int{} // seq -> app count

	for i := 0; i < 40; i++ {
		require.NoError(t, m.store.AddApp(&store.App{ID: fmt.Sprintf("id%02d", i), Name: fmt.Sprintf("app%02d", i), Port: 11000 + i, Host: store.HostLocal}))
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := m.syncState("")
			if err == nil {
				mu.Lock()
				seen[state.Seq] = len(state.Apps)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	seqs := make([]int64, 0, len(seen))
	for s := range seen {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i := 1; i < len(seqs); i++ {
		assert.GreaterOrEqual(t, seen[seqs[i]], seen[seqs[i-1]],
			"a higher sequence must never carry fewer apps than a lower one")
	}
}

// The mirror is what a node needs to do its job, and nothing more. An app's
// owner is a tenant fact the node has no use for -- it provisions accounts and
// containers, it never asks who owns one -- so shipping owner_id put a person's
// identity on every machine in the cluster for no reason.
func TestTheMirrorDoesNotShipAppOwnership(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{ID: "id1", Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: "user-42"}))

	state, err := m.syncState("")
	require.NoError(t, err)
	require.Len(t, state.Apps, 1)
	assert.Equal(t, "blog", state.Apps[0].Name)
	assert.Empty(t, state.Apps[0].OwnerID, "a node is not told who owns an app")

	// The registry itself still knows, of course.
	a, err := m.store.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "user-42", a.OwnerID)
}

// Port allocation rotates through the range so a just-freed port (and the uid
// derived from it) is left fallow: reusing one immediately started brand-new
// apps over their disk cap, because the dying subvolume's bytes were still on
// the budget qgroup. The rotation point lived only in memory, so a restart sent
// allocation back to the bottom of the range and reused the most recently freed
// ports first -- exactly the case it exists to avoid.
func TestPortRotationSurvivesARestart(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	first, err := m.allocatePort()
	require.NoError(t, err)
	m.releasePort(first)

	// A new control process over the same registry.
	second := newWiredManager(m.config, m.store, testServices(newFakeSystem(), newFakeRunner()))
	t.Cleanup(second.WaitBackground)
	next, err := second.allocatePort()
	require.NoError(t, err)
	assert.Greater(t, next, first, "allocation continues past the last grant instead of restarting at the bottom")
}
