package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImagePinning(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, ImageTag: "localhost/hostit-workspace:aaa"}))
	require.NoError(t, s.AddApp(&App{Name: "old", Port: 10001})) // empty tag: from before pinning

	// The pinned tag round-trips.
	got, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "localhost/hostit-workspace:aaa", got.ImageTag)

	// Backfill pins only the unpinned app; an already-pinned one keeps its tag.
	require.NoError(t, s.PinImageTags("localhost/hostit-workspace:bbb"))
	got, _ = s.App("old")
	assert.Equal(t, "localhost/hostit-workspace:bbb", got.ImageTag)
	got, _ = s.App("blog")
	assert.Equal(t, "localhost/hostit-workspace:aaa", got.ImageTag)

	// The in-use set covers every pinned tag, so image GC keeps them.
	inUse, err := s.ImageTagsInUse()
	require.NoError(t, err)
	assert.True(t, inUse["localhost/hostit-workspace:aaa"])
	assert.True(t, inUse["localhost/hostit-workspace:bbb"])
}
