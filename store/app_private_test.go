package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Public is the default, and deliberately so: every app that existed before
// this column did was reachable by URL, and the migration must not change what
// any of them mean.
func TestNewAppsArePublic(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "blog", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))

	a, err := s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private, "a new app is public")
}

func TestSetAppPrivate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "blog", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))

	require.NoError(t, s.SetAppPrivate("blog", true))
	a, err := s.App("blog")
	require.NoError(t, err)
	assert.True(t, a.Private)

	require.NoError(t, s.SetAppPrivate("blog", false))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private, "an app can be made public again")
}

// An app created private must BE private from its first row, not public for
// the moment between the insert and a follow-up update -- that gap is a window
// where the routing table would publish it.
func TestAppCanBeCreatedPrivate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now(), Private: true}))

	a, err := s.App("dash")
	require.NoError(t, err)
	assert.True(t, a.Private, "the flag is written by the insert, not by a second statement")
}

// The flag has to survive into every read path, or a caller that lists apps
// (the routing table builder, the dashboard) sees a private app as public.
func TestPrivateFlagIsReadEverywhere(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "open", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now(), UID: 100000}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "shut", Port: 10001, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now(), UID: 200000}))
	require.NoError(t, s.SetAppPrivate("shut", true))

	privacyOf := func(apps []*App) map[string]bool {
		out := make(map[string]bool, len(apps))
		for _, a := range apps {
			out[a.Name] = a.Private
		}
		return out
	}

	all, err := s.Apps()
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"open": false, "shut": true}, privacyOf(all), "Apps()")

	owned, err := s.AppsByOwner("u1")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"open": false, "shut": true}, privacyOf(owned), "AppsByOwner()")

	byUID, err := s.AppByUID(200000)
	require.NoError(t, err)
	assert.True(t, byUID.Private, "AppByUID()")
}
