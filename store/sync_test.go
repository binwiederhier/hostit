package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceNodeMirror(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// The mirror starts with stale rows from a previous sync.
	require.NoError(t, s.AddApp(&App{ID: "old1", Name: "gone", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddSnapshot(&Snapshot{ID: "snapold", AppName: "gone", CreatedAt: time.Now()}))

	apps := []*App{
		{ID: "a1", Name: "blog", Port: 10001, Host: HostLocal, OwnerID: "u1", DiskMB: 512, ImageTag: "img:1", UID: 1065536, PoweredOff: true, CreatedAt: time.Unix(1755000000, 0)},
		{ID: "a2", Name: "shop", Port: 10002, Host: HostLocal, CreatedAt: time.Unix(1755000001, 0)},
	}
	snaps := []*Snapshot{
		{ID: "snap1", AppName: "blog", Label: "save", CreatedAt: time.Unix(1755000002, 0), Auto: true},
	}
	require.NoError(t, s.ReplaceNodeMirror(apps, snaps))

	got, err := s.Apps()
	require.NoError(t, err)
	require.Len(t, got, 2)
	blog, err := s.App("blog")
	require.NoError(t, err)
	// Full-row fidelity: the node's loops read all of these.
	assert.Equal(t, "a1", blog.ID)
	assert.Equal(t, "u1", blog.OwnerID)
	assert.Equal(t, 512, blog.DiskMB)
	assert.Equal(t, "img:1", blog.ImageTag)
	assert.Equal(t, 1065536, blog.UID)
	assert.True(t, blog.PoweredOff)

	_, err = s.App("gone")
	require.Error(t, err, "stale rows are gone")
	gotSnaps, err := s.Snapshots("blog")
	require.NoError(t, err)
	require.Len(t, gotSnaps, 1)
	assert.Equal(t, "snap1", gotSnaps[0].ID)
	assert.True(t, gotSnaps[0].Auto)
	_, err = s.Snapshot("snapold")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

func TestReplaceAppSnapshots(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "blog", Port: 10001, Host: HostLocal}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "shop", Port: 10002, Host: HostLocal}))
	require.NoError(t, s.AddSnapshot(&Snapshot{ID: "s1", AppName: "blog", CreatedAt: time.Now()}))
	require.NoError(t, s.AddSnapshot(&Snapshot{ID: "keep", AppName: "shop", CreatedAt: time.Now()}))

	// The node reports blog's authoritative snapshot list: s1 was pruned, s2 is new.
	require.NoError(t, s.ReplaceAppSnapshots("blog", []*Snapshot{
		{ID: "s2", AppName: "blog", Label: "auto", CreatedAt: time.Unix(1755000002, 0), Auto: true},
	}))

	got, err := s.Snapshots("blog")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "s2", got[0].ID)
	other, err := s.Snapshots("shop")
	require.NoError(t, err)
	require.Len(t, other, 1, "other apps' snapshots are untouched")
}
