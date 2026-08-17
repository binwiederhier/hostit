package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppUIDPersistsAndBackfills(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal, UID: 1000000}))
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10001, Host: HostLocal}))

	a, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 1000000, a.UID, "the uid the node allocated is recorded")

	// A pre-existing row (uid 0) is backfilled once its uid is known
	require.NoError(t, s.SetAppUID("wiki", 1065536))
	b, err := s.App("wiki")
	require.NoError(t, err)
	assert.Equal(t, 1065536, b.UID)
}

// An app's uid is registry state, so it resolves without asking the local
// passwd file -- which knows nothing about an app that lives on another node.
func TestAppByUID(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "id1", Name: "remote", Port: 10005, Host: "worker-2", UID: 1327104}))

	a, err := s.AppByUID(1327104)
	require.NoError(t, err)
	assert.Equal(t, "remote", a.Name)

	_, err = s.AppByUID(999999)
	assert.ErrorIs(t, err, ErrAppNotFound)
}
