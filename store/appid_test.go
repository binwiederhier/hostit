package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddAppAssignsID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000}))
	require.NoError(t, s.AddApp(&App{Name: "shop", Port: 10001}))

	blog, err := s.App("blog")
	require.NoError(t, err)
	shop, err := s.App("shop")
	require.NoError(t, err)

	// Every app is born with a non-empty, distinct id.
	assert.NotEmpty(t, blog.ID)
	assert.NotEmpty(t, shop.ID)
	assert.NotEqual(t, blog.ID, shop.ID)
}

func TestAddAppKeepsExplicitID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "fixedid", Name: "blog", Port: 10000}))
	got, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "fixedid", got.ID)
}

func TestBackfillAppIDs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// Simulate two apps created before app ids by blanking their ids in place.
	require.NoError(t, s.AddApp(&App{Name: "old1", Port: 10000}))
	require.NoError(t, s.AddApp(&App{Name: "old2", Port: 10001}))
	_, err := s.db.Exec(`UPDATE app SET id = '' WHERE name IN ('old1', 'old2')`)
	require.NoError(t, err)

	require.NoError(t, s.BackfillAppIDs())

	old1, err := s.App("old1")
	require.NoError(t, err)
	old2, err := s.App("old2")
	require.NoError(t, err)
	assert.NotEmpty(t, old1.ID)
	assert.NotEmpty(t, old2.ID)
	assert.NotEqual(t, old1.ID, old2.ID)

	// Idempotent: a second run assigns nothing new and does not disturb existing ids.
	firstID := old1.ID
	require.NoError(t, s.BackfillAppIDs())
	old1, err = s.App("old1")
	require.NoError(t, err)
	assert.Equal(t, firstID, old1.ID)
}
