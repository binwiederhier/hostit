package app

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestTakeSnapshotRecordsAndSnapshotsSubvolume(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n") // btrfs, so snapshots are available
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	snap, err := m.TakeSnapshot("blog", "my save", false)
	require.NoError(t, err)
	assert.False(t, snap.Auto)
	assert.Equal(t, "my save", snap.Label)
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot -r "+m.appHome("blog")+" "+m.snapshotPath("blog", snap.ID))

	got, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, snap.ID, got[0].ID)
}

func TestTakeSnapshotUnavailableWithoutBtrfs(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	_, err := m.TakeSnapshot("blog", "", false)
	assert.ErrorIs(t, err, ErrSnapshotsUnavailable)
}

func TestRollbackTakesAutoLabelledSafetySnapshot(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError) // no container yet -> Up creates one
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")

	target, err := m.TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	require.NoError(t, m.Rollback("blog", target.ID))

	snaps, err := m.ListSnapshots("blog")
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
}

// A rollback must build the restored home from the target BEFORE taking the
// safety snapshot, because the safety snapshot triggers retention pruning that
// could otherwise delete the very target being restored (a data-loss bug).
func TestRollbackStagesTargetBeforeSafetySnapshot(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")

	target, err := m.TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	r.reset()
	require.NoError(t, m.Rollback("blog", target.ID))

	ran := r.ran()
	stagedIdx := strings.Index(ran, "btrfs subvolume snapshot "+m.snapshotPath("blog", target.ID)+" "+m.appHome("blog")+rollbackStagedSuffix)
	safetyIdx := strings.Index(ran, "btrfs subvolume snapshot -r "+m.appHome("blog")+" ")
	require.GreaterOrEqual(t, stagedIdx, 0, "the target must be staged into a writable copy for rollback")
	require.GreaterOrEqual(t, safetyIdx, 0, "a safety snapshot must be taken")
	assert.Less(t, stagedIdx, safetyIdx, "the target must be staged before the safety snapshot (which can prune it)")
}

// A rollback should record exactly one auto snapshot (the safety one), not also a
// redundant pre-deploy snapshot from the Up() it runs afterwards.
func TestRollbackTakesExactlyOneSafetySnapshot(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")

	target, err := m.TakeSnapshot("blog", "target", false) // manual, so autos are only the rollback's
	require.NoError(t, err)
	require.NoError(t, m.Rollback("blog", target.ID))

	snaps, err := m.ListSnapshots("blog")
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
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	snap, err := m.TakeSnapshot("blog", "save", false)
	require.NoError(t, err)

	require.NoError(t, m.DeleteSnapshot("blog", snap.ID))
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+m.snapshotPath("blog", snap.ID))

	got, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	assert.Empty(t, got, "the record is gone once the snapshot is deleted")
}

func TestDeleteSnapshotWrongAppIsNotFound(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))

	snap, err := m.TakeSnapshot("blog", "", false)
	require.NoError(t, err)
	assert.ErrorIs(t, m.DeleteSnapshot("other", snap.ID), store.ErrSnapshotNotFound)
}

func TestDeleteSnapshotUnavailableWithoutBtrfs(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	assert.ErrorIs(t, m.DeleteSnapshot("blog", "nope"), ErrSnapshotsUnavailable)
}
