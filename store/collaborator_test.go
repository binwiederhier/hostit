package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

func newCollabStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCollaboratorsRoundTrip(t *testing.T) {
	t.Parallel()
	s := newCollabStore(t)
	require.NoError(t, s.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: "u_owner"}))
	a, err := s.App("blog")
	require.NoError(t, err)
	alice := &store.User{Email: "alice@example.com", Name: "Alice", Role: store.RoleUser, Status: store.StatusActive}
	require.NoError(t, s.AddUser(alice))
	bob := &store.User{Email: "bob@example.com", Name: "Bob", Role: store.RoleUser, Status: store.StatusActive}
	require.NoError(t, s.AddUser(bob))

	// Add both; the list joins the user table so callers get emails, not ids.
	require.NoError(t, s.AddAppCollaborator(a.ID, alice.ID))
	require.NoError(t, s.AddAppCollaborator(a.ID, bob.ID))
	users, err := s.AppCollaborators(a.ID)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "alice@example.com", users[0].Email, "sorted by email")
	assert.Equal(t, "bob@example.com", users[1].Email)

	// Adding twice is idempotent, not an error (the grant already holds).
	require.NoError(t, s.AddAppCollaborator(a.ID, alice.ID))
	users, err = s.AppCollaborators(a.ID)
	require.NoError(t, err)
	assert.Len(t, users, 2)

	// The reverse direction drives the dashboard listing.
	apps, err := s.AppsByCollaborator(alice.ID)
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
	assert.True(t, s.IsAppCollaborator(a.ID, alice.ID))
	assert.False(t, s.IsAppCollaborator(a.ID, "u_stranger"))

	require.NoError(t, s.RemoveAppCollaborator(a.ID, alice.ID))
	assert.False(t, s.IsAppCollaborator(a.ID, alice.ID))
	users, err = s.AppCollaborators(a.ID)
	require.NoError(t, err)
	assert.Len(t, users, 1)
}

func TestCollaboratorRowsDieWithTheAppAndTheUser(t *testing.T) {
	t.Parallel()
	s := newCollabStore(t)
	require.NoError(t, s.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: "u_owner"}))
	a, err := s.App("blog")
	require.NoError(t, err)
	alice := &store.User{Email: "alice@example.com", Name: "Alice", Role: store.RoleUser, Status: store.StatusActive}
	require.NoError(t, s.AddUser(alice))
	require.NoError(t, s.AddAppCollaborator(a.ID, alice.ID))

	// Deleting the app drops its grants.
	require.NoError(t, s.RemoveApp("blog"))
	apps, err := s.AppsByCollaborator(alice.ID)
	require.NoError(t, err)
	assert.Empty(t, apps)

	// Deleting a user drops their grants everywhere.
	require.NoError(t, s.AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: "u_owner"}))
	w, err := s.App("wiki")
	require.NoError(t, err)
	require.NoError(t, s.AddAppCollaborator(w.ID, alice.ID))
	require.NoError(t, s.RemoveUser(alice.ID))
	users, err := s.AppCollaborators(w.ID)
	require.NoError(t, err)
	assert.Empty(t, users)
}
