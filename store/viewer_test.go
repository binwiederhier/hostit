package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addViewerTestApp(t *testing.T, s *Store) *App {
	t.Helper()
	a := &App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now(), Private: true}
	require.NoError(t, s.AddApp(a))
	require.NoError(t, s.AddUser(&User{ID: "u2", Email: "viewer@example.com", Status: StatusActive, CreatedAt: time.Now()}))
	return a
}

func TestViewersRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	a := addViewerTestApp(t, s)

	assert.False(t, s.IsAppViewer(a.ID, "u2"), "nobody is a viewer to start with")
	require.NoError(t, s.AddAppViewer(a.ID, "u2"))
	assert.True(t, s.IsAppViewer(a.ID, "u2"))

	viewers, err := s.AppViewers(a.ID)
	require.NoError(t, err)
	require.Len(t, viewers, 1)
	assert.Equal(t, "viewer@example.com", viewers[0].Email)

	require.NoError(t, s.RemoveAppViewer(a.ID, "u2"))
	assert.False(t, s.IsAppViewer(a.ID, "u2"))
}

// Granting twice is the same as granting once: the UI adds by email, and
// re-adding somebody who already has access should not be an error.
func TestAddingAViewerTwiceIsANoOp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	a := addViewerTestApp(t, s)

	require.NoError(t, s.AddAppViewer(a.ID, "u2"))
	require.NoError(t, s.AddAppViewer(a.ID, "u2"))
	viewers, err := s.AppViewers(a.ID)
	require.NoError(t, err)
	assert.Len(t, viewers, 1)
}

// Viewing and collaborating are separate grants on purpose, so holding one
// says nothing about the other.
func TestViewersAndCollaboratorsAreSeparateGrants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	a := addViewerTestApp(t, s)
	require.NoError(t, s.AddUser(&User{ID: "u3", Email: "collab@example.com", Status: StatusActive, CreatedAt: time.Now()}))

	require.NoError(t, s.AddAppViewer(a.ID, "u2"))
	require.NoError(t, s.AddAppCollaborator(a.ID, "u3"))

	assert.True(t, s.IsAppViewer(a.ID, "u2"))
	assert.False(t, s.IsAppCollaborator(a.ID, "u2"), "a viewer is not a collaborator")
	assert.True(t, s.IsAppCollaborator(a.ID, "u3"))
	assert.False(t, s.IsAppViewer(a.ID, "u3"), "a collaborator holds no separate viewer row")

	viewers, err := s.AppViewers(a.ID)
	require.NoError(t, err)
	require.Len(t, viewers, 1)
	assert.Equal(t, "viewer@example.com", viewers[0].Email, "the viewer list does not include collaborators")
}

// A deleted app or user must not leave grants behind: the ids are reused for
// new apps, and a stale row would silently hand somebody access.
func TestViewerGrantsAreCleanedUp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	a := addViewerTestApp(t, s)
	require.NoError(t, s.AddAppViewer(a.ID, "u2"))

	require.NoError(t, s.RemoveApp(a.Name))
	viewers, err := s.AppViewers(a.ID)
	require.NoError(t, err)
	assert.Empty(t, viewers, "deleting the app dropped its viewers")

	b := &App{ID: "a2", Name: "other", Port: 10001, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}
	require.NoError(t, s.AddApp(b))
	require.NoError(t, s.AddAppViewer(b.ID, "u2"))
	require.NoError(t, s.RemoveUser("u2"))
	viewers, err = s.AppViewers(b.ID)
	require.NoError(t, err)
	assert.Empty(t, viewers, "deleting the user dropped their viewer grants")
}

// The sets the proxy enforces on: both grants collapse into "may open it",
// suspended accounts are excluded, and it is two queries for the whole
// registry rather than two per app.
func TestAccessSets(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	a := addViewerTestApp(t, s) // app a1, plus active user u2
	require.NoError(t, s.AddUser(&User{ID: "u3", Email: "collab@example.com", Status: StatusActive, CreatedAt: time.Now()}))
	require.NoError(t, s.AddUser(&User{ID: "u4", Email: "gone@example.com", Status: StatusDenied, CreatedAt: time.Now()}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "other", Port: 10001, Host: HostLocal, OwnerID: "u1", CreatedAt: time.Now()}))

	require.NoError(t, s.AddAppViewer(a.ID, "u2"))
	require.NoError(t, s.AddAppCollaborator(a.ID, "u3"))
	require.NoError(t, s.AddAppViewer(a.ID, "u4")) // suspended
	require.NoError(t, s.AddAppCollaborator("a2", "u3"))

	sets, err := s.AccessSets()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u2", "u3"}, sets["a1"], "both grants, and not the suspended one")
	assert.ElementsMatch(t, []string{"u3"}, sets["a2"])
	assert.Empty(t, sets["nosuchapp"])
}

func TestActiveAdmins(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddUser(&User{ID: "a1", Email: "admin@example.com", Role: RoleAdmin, Status: StatusActive, CreatedAt: time.Now()}))
	require.NoError(t, s.AddUser(&User{ID: "a2", Email: "exadmin@example.com", Role: RoleAdmin, Status: StatusDenied, CreatedAt: time.Now()}))
	require.NoError(t, s.AddUser(&User{ID: "u1", Email: "user@example.com", Role: RoleUser, Status: StatusActive, CreatedAt: time.Now()}))

	admins, err := s.ActiveAdmins()
	require.NoError(t, err)
	assert.Equal(t, []string{"a1"}, admins, "only admins, and only active ones")
}
