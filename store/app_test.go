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

// Per-app limit OVERRIDES: 0 means "no override" (memory/disk inherit the
// owner's defaults; CPU stays uncapped), so a fresh app carries three zeros
// and an admin's explicit numbers survive restarts -- unlike the derived
// limits, which live only in control's memory.
func TestAppLimitOverridesRoundtrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal, UID: 1000000}))

	a, err := s.App("blog")
	require.NoError(t, err)
	assert.Zero(t, a.MemoryLimitMB)
	assert.Zero(t, a.DiskLimitMB)
	assert.Zero(t, a.CPUMilli)

	require.NoError(t, s.UpdateAppLimits("blog", 512, 4096, 1500))
	for _, load := range map[string]func() (*App, error){
		"App":      func() (*App, error) { return s.App("blog") },
		"AppByUID": func() (*App, error) { return s.AppByUID(1000000) },
		"Apps": func() (*App, error) {
			apps, err := s.Apps()
			if err != nil || len(apps) == 0 {
				return nil, err
			}
			return apps[0], nil
		},
	} {
		a, err := load()
		require.NoError(t, err)
		assert.Equal(t, 512, a.MemoryLimitMB)
		assert.Equal(t, 4096, a.DiskLimitMB)
		assert.Equal(t, 1500, a.CPUMilli)
	}

	// Clearing returns to "no override".
	require.NoError(t, s.UpdateAppLimits("blog", 0, 0, 0))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.Zero(t, a.MemoryLimitMB)

	assert.ErrorIs(t, s.UpdateAppLimits("nosuch", 1, 1, 1), ErrAppNotFound)
}
