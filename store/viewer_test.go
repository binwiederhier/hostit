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
