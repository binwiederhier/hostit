package control

import (
	"os"
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
	rec := &recordingAgent{NodeAgent: m}
	m.SetNodeAgent(rec)

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
	rec := &recordingAgent{NodeAgent: m}
	m.SetNodeAgent(rec)

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
	m.SetControlSink(sink)

	require.NoError(t, m.Down("blog"))
	off, ok := sink.power["blog"]
	require.True(t, ok, "a poweroff the node performs must reach control")
	assert.True(t, off)

	_, err := m.PowerOn("blog")
	require.NoError(t, err)
	assert.False(t, sink.power["blog"])
}

func TestSnapshotMutationsNotifyTheSink(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "300\n")
	createTestApp(t, m, "blog")
	sink := newFakeSink()
	m.SetControlSink(sink)

	snap, err := m.TakeSnapshot("blog", "save", false)
	require.NoError(t, err)
	ids := func() []string {
		out := make([]string, 0)
		for _, rec := range sink.snapshots["blog"] {
			out = append(out, rec.ID)
		}
		return out
	}
	assert.Contains(t, ids(), snap.ID, "the record list travels with the callback")

	require.NoError(t, m.DeleteSnapshot("blog", snap.ID))
	assert.NotContains(t, ids(), snap.ID)
}

func TestCreatePushesMirrorBeforeProvision(t *testing.T) {
	t.Parallel()
	// The node's reconcile treats unknown ids as orphans: if machine state
	// (subvolume, user) appears before the mirror knows the id, a concurrent
	// reconcile on the node tears the half-built app down (seen live on the
	// stage two-node setup). The row and the mirror push must come FIRST.
	m, _, _ := newTestDeployManager(t)
	rec := &orderRecordingAgent{recordingAgent: recordingAgent{NodeAgent: m}}
	m.SetNodeAgent(rec)

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
	rec := &fileRoutingAgent{NodeAgent: m}
	m.SetNodeAgent(rec)

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
	m.SetNodeAgent(&statesRoutingAgent{NodeAgent: m, states: map[string]State{
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
	rec := &recordingAgent{NodeAgent: m}
	m.SetNodeAgent(rec)

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
