package app

import (
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
