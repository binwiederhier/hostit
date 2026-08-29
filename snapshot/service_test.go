package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/run"
	"heckel.io/hostit/system/systemd"
)

func TestSnapshotID(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 8, 7, 14, 5, 1, 0, time.UTC)
	assert.Equal(t, "20260807-140501-auto", snapshotID(ts, "auto"))
}

func TestTakeSnapshotRecordsAndSnapshotsSubvolume(t *testing.T) {
	t.Parallel()
	svc, h, r, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	snap, err := svc.TakeSnapshot("blog", "my save", false)
	require.NoError(t, err)
	assert.False(t, snap.Auto)
	assert.Equal(t, "my save", snap.Label)
	// Created with -i straight into the app's disk budget: extents shared between
	// home and snapshot would otherwise be reachable outside the group and stop
	// counting as the group's exclusive bytes -- home data would leak out of the
	// cap. (-i also avoids the post-hoc assign's rescan window.)
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot -r -i 1/1000 "+h.AppSubvolume("blog")+" "+h.SnapshotPath("blog", snap.ID))

	got, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, snap.ID, got[0].ID)
}

func TestTakeSnapshotAbortsWhenPreHookFails(t *testing.T) {
	t.Parallel()
	svc, h, _, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	// A pre hook that exits non-zero aborts the snapshot, so a torn state is never
	// captured and nothing is recorded.
	h.pre = "flush-db"
	h.hookCode = 1

	_, err := svc.TakeSnapshot("blog", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre hook failed")
	snaps, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	assert.Empty(t, snaps)
}

func TestRollbackTakesAutoLabelledSafetySnapshot(t *testing.T) {
	t.Parallel()
	svc, h, _, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(h.AppSubvolume("blog"), 0o755))

	target, err := svc.TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	require.NoError(t, svc.Rollback("blog", target.ID))

	snaps, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	want := "Before rolling back to snapshot " + target.ID
	var safety *store.Snapshot
	for _, s := range snaps {
		if s.Label == want {
			safety = s
		}
	}
	require.NotNil(t, safety, "a labelled safety snapshot must be taken before rolling back")
	assert.True(t, safety.Auto, "the safety snapshot is tagged Auto")
	assert.Equal(t, []string{"blog"}, h.upCalled, "the app is brought back up after the rollback")
}

// A rollback must build the restored home from the target BEFORE taking the
// safety snapshot, because the safety snapshot triggers retention pruning that
// could otherwise delete the very target being restored (a data-loss bug).
func TestRollbackStagesTargetBeforeSafetySnapshot(t *testing.T) {
	t.Parallel()
	svc, h, r, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(h.AppSubvolume("blog"), 0o755))

	target, err := svc.TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	r.reset()
	require.NoError(t, svc.Rollback("blog", target.ID))

	ran := r.ran()
	stagedIdx := strings.Index(ran, "btrfs subvolume snapshot -i 1/1000 "+h.SnapshotPath("blog", target.ID)+" "+h.AppSubvolume("blog")+rollbackStagedSuffix)
	safetyIdx := strings.Index(ran, "btrfs subvolume snapshot -r -i 1/1000 "+h.AppSubvolume("blog")+" ")
	require.GreaterOrEqual(t, stagedIdx, 0, "the target must be staged into a writable copy for rollback")
	require.GreaterOrEqual(t, safetyIdx, 0, "a safety snapshot must be taken")
	assert.Less(t, stagedIdx, safetyIdx, "the target must be staged before the safety snapshot (which can prune it)")

	// The staged writable copy becomes the app's home after the swap; the -i in
	// the staged snapshot above puts it in the app's disk budget at birth.
}

// A rollback should record exactly one auto snapshot (the safety one), not also a
// redundant pre-deploy snapshot from the up path it runs afterwards (which is why
// Host.Up must not take a pre-deploy snapshot).
func TestRollbackTakesExactlyOneSafetySnapshot(t *testing.T) {
	t.Parallel()
	svc, h, _, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(h.AppSubvolume("blog"), 0o755))

	target, err := svc.TakeSnapshot("blog", "target", false) // manual, so autos are only the rollback's
	require.NoError(t, err)
	require.NoError(t, svc.Rollback("blog", target.ID))

	snaps, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	autos := 0
	for _, s := range snaps {
		if s.Auto {
			autos++
		}
	}
	assert.Equal(t, 1, autos, "rollback should add exactly one auto (safety) snapshot")
}

func TestDeleteSnapshotRemovesSubvolumeAndRecord(t *testing.T) {
	t.Parallel()
	svc, h, r, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	snap, err := svc.TakeSnapshot("blog", "save", false)
	require.NoError(t, err)

	require.NoError(t, svc.DeleteSnapshot("blog", snap.ID))
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+h.SnapshotPath("blog", snap.ID))

	got, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	assert.Empty(t, got, "the record is gone once the snapshot is deleted")
}

func TestDeleteSnapshotWrongAppIsNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, st.AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))

	snap, err := svc.TakeSnapshot("blog", "", false)
	require.NoError(t, err)
	assert.ErrorIs(t, svc.DeleteSnapshot("other", snap.ID), store.ErrSnapshotNotFound)
}

// newTestService builds a snapshot Service over a fake runner-backed btrfs/systemd/
// container stack, a real store and a fake Host, so the service's orchestration can
// be exercised without an actual btrfs filesystem or control.Manager.
func newTestService(t *testing.T) (*Service, *fakeHost, *fakeRunner, *store.Store) {
	t.Helper()
	r := newFakeRunner()
	st, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	h := &fakeHost{base: t.TempDir()}
	svc := New(btrfs.New(r), systemd.New(r), podman.New(r), st, h)
	return svc, h, r, st
}

// fakeHost stands in for control.Manager: it hands out tmpdir-based paths, records the
// Up and BudgetGroup callbacks and returns canned snapshot-hook results.
type fakeHost struct {
	base      string
	upCalled  []string
	pre, post string // Snapshot hook commands
	hookCode  int    // Exit code RunHook reports
	hookErr   error  // Error RunHook reports (command could not run)
}

var _ Host = (*fakeHost)(nil)

func (h *fakeHost) LockApp(string) func()     { return func() {} }
func (h *fakeHost) BudgetGroup(string) string { return "1/1000" }
func (h *fakeHost) Up(name string) error      { h.upCalled = append(h.upCalled, name); return nil }
func (h *fakeHost) StateChanged(string)       {}
func (h *fakeHost) SnapshotsChanged(string)   {}
func (h *fakeHost) SnapshotHooks(string) (string, string) {
	return h.pre, h.post
}
func (h *fakeHost) RunHook(string, string, time.Duration) (int, error) { return h.hookCode, h.hookErr }
func (h *fakeHost) AppSubvolume(name string) string                    { return filepath.Join(h.base, name) }
func (h *fakeHost) SnapshotsRoot(name string) string {
	return filepath.Join(h.base, ".snapshots", name)
}
func (h *fakeHost) SnapshotPath(name, id string) string {
	return filepath.Join(h.SnapshotsRoot(name), id)
}
func (h *fakeHost) UnitName(name string) string      { return "hostit-app@" + name }
func (h *fakeHost) ContainerName(name string) string { return "hostit-app-" + name }
func (h *fakeHost) UIDForPort(port int) int          { return 100000 + port }
func (h *fakeHost) Chown(string, int) error          { return nil }

// fakeRunner records commands and, like the real btrfs tool, materializes the
// destination directory of a subvolume create/snapshot so staged paths exist.
type fakeRunner struct {
	mu       sync.Mutex
	commands []string
	outputs  map[string]string
	errs     map[string]error
}

var _ run.Runner = (*fakeRunner)(nil)

func newFakeRunner() *fakeRunner {
	return &fakeRunner{outputs: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	return f.Run(args...)
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	if len(args) >= 4 && args[0] == "btrfs" && args[1] == "subvolume" &&
		(args[2] == "create" || args[2] == "snapshot") {
		_ = os.MkdirAll(args[len(args)-1], 0o755)
	}
	for substr, err := range f.errs {
		if strings.Contains(cmd, substr) {
			return "", err
		}
	}
	for substr, out := range f.outputs {
		if strings.Contains(cmd, substr) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) ran() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.commands, "\n")
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = nil
}

// Retention is control's decision now: a take must record its snapshot and
// NOTHING else -- no matter how many records already exist, the node never
// prunes on its own (control applies the policy and commands deletions).
func TestTakeSnapshotDoesNotPrune(t *testing.T) {
	t.Parallel()
	svc, _, r, st := newTestService(t)
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	// Far more automatic records than the retention policy keeps.
	now := time.Now()
	for i := 0; i < 80; i++ {
		require.NoError(t, st.AddSnapshot(&store.Snapshot{
			ID:        snapshotID(now.Add(-time.Duration(i)*time.Hour), "seed"),
			AppName:   "blog",
			Label:     AutoSnapshotLabel,
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
			Auto:      true,
		}))
	}
	r.reset()

	_, err := svc.TakeSnapshot("blog", AutoSnapshotLabel, true)
	require.NoError(t, err)

	snaps, err := svc.ListSnapshots("blog")
	require.NoError(t, err)
	assert.Len(t, snaps, 81, "all 80 seeds and the new snapshot survive the take")
	assert.NotContains(t, r.ran(), "subvolume delete", "no prune deletions on take")
}
