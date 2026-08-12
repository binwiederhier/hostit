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
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10001, ImageTag: "localhost/hostit-workspace:bbb"}))

	// The pinned tag round-trips.
	got, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "localhost/hostit-workspace:aaa", got.ImageTag)

	// The in-use set covers every pinned tag, so image GC keeps them.
	inUse, err := s.ImageTagsInUse()
	require.NoError(t, err)
	assert.True(t, inUse["localhost/hostit-workspace:aaa"])
	assert.True(t, inUse["localhost/hostit-workspace:bbb"])
}
