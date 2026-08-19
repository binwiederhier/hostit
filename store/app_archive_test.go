package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Archiving is recorded intent, distinct from powered_off: an owner flips power
// freely, while archived is a state the app has to be brought out of before it
// can run at all.
func TestArchiveIsSeparateFromPoweredOff(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "blog", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))

	a, err := s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Archived, "a new app is not archived")
	assert.False(t, a.PoweredOff)

	require.NoError(t, s.SetAppArchived("blog", true))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.True(t, a.Archived)
	assert.False(t, a.PoweredOff, "archiving does not itself set the power flag in the registry")

	require.NoError(t, s.SetAppArchived("blog", false))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Archived, "unarchiving restores an ordinary app")
}

// The flag survives into the list the dashboard and the node mirror read, not
// just the single-app lookup.
func TestArchivedFlagIsListed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "kept", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "shelved", Port: 10001, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))
	require.NoError(t, s.SetAppArchived("shelved", true))

	apps, err := s.Apps()
	require.NoError(t, err)
	byName := map[string]bool{}
	for _, a := range apps {
		byName[a.Name] = a.Archived
	}
	assert.False(t, byName["kept"])
	assert.True(t, byName["shelved"])
}
