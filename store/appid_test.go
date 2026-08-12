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
