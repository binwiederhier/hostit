package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A connection belongs to a user and is reusable across their apps: connecting
// Google once is what lets three of their apps read the same calendar.
func TestConnectionsRoundTripPerUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.SaveConnection(&Connection{
		UserID: "u1", Provider: "google", Kind: ConnectionOAuth,
		Secret: "refresh-token", Scopes: "calendar.readonly", CreatedAt: time.Now(),
	}))

	got, err := s.Connection("u1", "google")
	require.NoError(t, err)
	assert.Equal(t, "refresh-token", got.Secret)
	assert.Equal(t, ConnectionOAuth, got.Kind)

	// Another user's connection to the same provider is a different row.
	_, err = s.Connection("u2", "google")
	assert.ErrorIs(t, err, ErrConnectionNotFound)

	list, err := s.Connections("u1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "google", list[0].Provider)
}

// Reconnecting replaces the credential rather than accumulating rows -- an
// owner who re-runs the consent flow expects one Google connection, not two.
func TestReconnectingReplacesTheCredential(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	for _, secret := range []string{"first", "second"} {
		require.NoError(t, s.SaveConnection(&Connection{
			UserID: "u1", Provider: "github", Kind: ConnectionStatic, Secret: secret, CreatedAt: time.Now(),
		}))
	}
	list, err := s.Connections("u1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "second", list[0].Secret)
}

func TestDisconnectRemovesItAndItsGrants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.SaveConnection(&Connection{UserID: "u1", Provider: "google", Kind: ConnectionOAuth, Secret: "r", CreatedAt: time.Now()}))
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))
	require.NoError(t, s.GrantConnection("a1", "google"))

	require.NoError(t, s.DeleteConnection("u1", "google"))
	_, err := s.Connection("u1", "google")
	assert.ErrorIs(t, err, ErrConnectionNotFound)

	// The grant must go with it: a grant naming a connection that no longer
	// exists would silently come back to life on reconnect.
	granted, err := s.AppConnections("a1")
	require.NoError(t, err)
	assert.Empty(t, granted)
}

// A grant is per app, and an app only reaches what it was granted.
func TestGrantsArePerApp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.SaveConnection(&Connection{UserID: "u1", Provider: "google", Kind: ConnectionOAuth, Secret: "r", CreatedAt: time.Now()}))
	require.NoError(t, s.SaveConnection(&Connection{UserID: "u1", Provider: "github", Kind: ConnectionStatic, Secret: "t", CreatedAt: time.Now()}))
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "other", Port: 10001, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))

	require.NoError(t, s.GrantConnection("a1", "google"))
	granted, err := s.AppConnections("a1")
	require.NoError(t, err)
	assert.Equal(t, []string{"google"}, granted, "only what was granted")

	granted, err = s.AppConnections("a2")
	require.NoError(t, err)
	assert.Empty(t, granted, "another app of the same owner gets nothing by default")

	require.NoError(t, s.RevokeConnection("a1", "google"))
	granted, err = s.AppConnections("a1")
	require.NoError(t, err)
	assert.Empty(t, granted)
}
