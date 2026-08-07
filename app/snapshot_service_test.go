package app

import (
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
