package control

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestAppGrantRoundTrip(t *testing.T) {
	t.Parallel()
	g := newGrantManager("session-key")

	value, err := g.encode("blog", "u1")
	require.NoError(t, err)
	app, userID, err := g.decode(value)
	require.NoError(t, err)
	assert.Equal(t, "blog", app)
	assert.Equal(t, "u1", userID)
}

// A grant is signed with a key DERIVED from the session key, never the session
// key itself. Without the separation, a stolen session cookie would verify as a
// grant (and the reverse), and the two have very different blast radii.
func TestAppGrantIsNotASession(t *testing.T) {
	t.Parallel()
	key := "session-key"
	sessions, grants := newSessionManager(key), newGrantManager(key)

	session, err := sessions.encode("u1")
	require.NoError(t, err)
	_, _, err = grants.decode(session)
	assert.Error(t, err, "a session cookie must not verify as a grant")

	grant, err := grants.encode("blog", "u1")
	require.NoError(t, err)
	_, err = sessions.decode(grant)
	assert.Error(t, err, "a grant must not verify as a session")
}

func TestAppGrantRejectsTampering(t *testing.T) {
	t.Parallel()
	g := newGrantManager("session-key")
	value, err := g.encode("blog", "u1")
	require.NoError(t, err)

	// Re-pointing a valid grant at another app is the attack that matters: the
	// cookie lives on a hostname whose content the app's owner controls.
	tampered := "secret" + value[len("blog"):]
	_, _, err = g.decode(tampered)
	assert.Error(t, err)

	_, _, err = g.decode(value + "x")
	assert.Error(t, err)
	_, _, err = g.decode("garbage")
	assert.Error(t, err)
}

func TestAppGrantExpires(t *testing.T) {
	t.Parallel()
	g := newGrantManager("session-key")
	g.ttl = -time.Minute

	value, err := g.encode("blog", "u1")
	require.NoError(t, err)
	_, _, err = g.decode(value)
	assert.ErrorIs(t, err, errInvalidGrant)
}

// The visibility decision itself: who may reach a private app.
func TestMayViewApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	friend := newActiveTestUser(t, s, "friend@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))

	app := &store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}
	require.NoError(t, s.apps.Store().AddApp(app))
	require.NoError(t, s.apps.Store().AddAppCollaborator(app.ID, friend.ID))

	assert.True(t, s.mayViewApp(&caller{user: owner}, app), "the owner")
	assert.True(t, s.mayViewApp(&caller{user: friend}, app), "a collaborator")
	assert.True(t, s.mayViewApp(&caller{user: admin}, app), "an admin")
	assert.True(t, s.mayViewApp(&caller{globalAdmin: true}, app), "the admin token")
	assert.False(t, s.mayViewApp(&caller{user: stranger}, app), "a stranger")
	assert.False(t, s.mayViewApp(nil, app), "nobody")
	assert.False(t, s.mayViewApp(&caller{}, app), "an empty caller")
}

// An app-scoped token is meant to be pasted into that app's own agent, so it
// speaks for its app and for nothing else.
func TestMayViewAppWithAppScopedToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	app := &store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}
	require.NoError(t, s.apps.Store().AddApp(app))

	assert.True(t, s.mayViewApp(&caller{appScope: "dash"}, app), "its own app")
	assert.False(t, s.mayViewApp(&caller{appScope: "other"}, app), "somebody else's app")
}

// A suspended or still-pending account keeps no access, however it authenticates.
func TestMayViewAppRequiresAnActiveAccount(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	app := &store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}
	require.NoError(t, s.apps.Store().AddApp(app))
	owner.Status = store.StatusDenied
	require.NoError(t, s.users.Update(owner))

	suspended, err := s.users.User(owner.ID)
	require.NoError(t, err)
	assert.False(t, s.mayViewApp(&caller{user: suspended}, app), "an owner whose account was suspended")
}
