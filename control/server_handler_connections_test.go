package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/store"
)

// connectStatic connects a static provider for a user, the way the profile page
// does, and returns the app-scoped token for one of their apps.
func connectAndGrant(t *testing.T, s *Server, u *store.User, appName, provider string, grant bool) {
	t.Helper()
	token := accountToken(t, s, u)
	body := `{"token":"ghp_secret"}`
	if provider == "imap" {
		body = `{"host":"imap.example.com:993","username":"me@example.com","password":"app-password"}`
	}
	require.Equal(t, http.StatusOK, request(t, s.API(), "PUT", "/api/connections/"+provider, body, token).Code)
	if grant {
		require.Equal(t, http.StatusOK,
			request(t, s.API(), "PUT", "/api/apps/"+appName+"/connections/"+provider, "", token).Code)
	}
}

// The whole point, end to end: an owner connects an account, grants it to one
// app, and that app gets a usable credential -- without the credential ever
// being written into its environment or its files.
func TestGrantedAppGetsAToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, CreatedAt: time.Now()}))
	connectAndGrant(t, s, u, "dash", "github", true)

	a, err := s.apps.Store().App("dash")
	require.NoError(t, err)
	token, err := s.connections.tokenFor(t.Context(), a, "github")
	require.NoError(t, err)
	assert.Equal(t, "ghp_secret", token.AccessToken, "a static credential is handed back as-is")
	assert.Equal(t, "github", token.Provider)
}

// An app that was not granted the connection is refused even though its owner
// has connected it. The grant is the control, not the connection.
func TestUngrantedAppIsRefused(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, CreatedAt: time.Now()}))
	connectAndGrant(t, s, u, "dash", "github", false)

	a, err := s.apps.Store().App("dash")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(t.Context(), a, "github")
	assert.ErrorIs(t, err, errNotGranted)
}

// Another owner's app cannot reach this owner's connection, even granted: the
// token is looked up against the APP's owner, so there is no way to name
// someone else's account.
func TestAnotherOwnersAppCannotReachTheConnection(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	other := newActiveTestUser(t, s, "other@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, CreatedAt: time.Now()}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a2", Name: "theirs", Port: 10001, Host: store.HostLocal, OwnerID: other.ID, CreatedAt: time.Now()}))
	connectAndGrant(t, s, owner, "dash", "github", true)

	// Force the grant onto the other owner's app, the worst case a bug could
	// produce, and check the lookup still refuses.
	require.NoError(t, s.apps.Store().GrantConnection("a2", "github"))
	theirs, err := s.apps.Store().App("theirs")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(t.Context(), theirs, "github")
	assert.ErrorIs(t, err, errNotConnected, "it resolves against the app's own owner, who connected nothing")
}

// Disconnecting revokes access immediately, and takes the grants with it.
func TestDisconnectingCutsTheAppOff(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, CreatedAt: time.Now()}))
	connectAndGrant(t, s, u, "dash", "github", true)

	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/connections/github", "", token).Code)
	a, err := s.apps.Store().App("dash")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(t.Context(), a, "github")
	assert.Error(t, err, "the app loses access the moment the owner disconnects")
}

// The credential is not in the database in the clear, and the API never returns
// it -- the list is for showing what is connected, not for reading it back.
func TestTheCredentialIsNeverReadableBack(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, CreatedAt: time.Now()}))
	connectAndGrant(t, s, u, "dash", "imap", true)

	stored, err := s.apps.Store().Connection(u.ID, "imap")
	require.NoError(t, err)
	assert.NotContains(t, stored.Secret, "app-password", "the row holds ciphertext")
	assert.Contains(t, stored.Meta, "imap.example.com:993", "non-secret context stays readable")

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), "app-password", "the API does not hand it back")

	var list []apiConnection
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	var imap apiConnection
	for _, c := range list {
		if c.Provider == "imap" {
			imap = c
		}
	}
	assert.True(t, imap.Connected)
	assert.Equal(t, connections.KindStatic, imap.Kind)
}

// Granting something the owner has not connected is refused, so a grant can
// never name a credential the token endpoint cannot produce.
func TestCannotGrantWhatIsNotConnected(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, CreatedAt: time.Now()}))

	rr := request(t, s.API(), "PUT", "/api/apps/dash/connections/github", "", token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// An admin token is not a person: it authenticates as the instance, with no
// user account behind it. A connection belongs to a user, so these handlers
// have to refuse it -- reaching for c.user.ID here panicked the daemon, which
// every test using an account token missed.
func TestConnectionHandlersRefuseATokenWithNoUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	admin := s.config.AdminToken

	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/connections", ""},
		{"PUT", "/api/connections/imap", `{"host":"h","username":"u","password":"p"}`},
		{"DELETE", "/api/connections/imap", ""},
		{"POST", "/api/connections/google/start", ""},
	} {
		rr := request(t, s.API(), tc.method, tc.path, tc.body, admin)
		assert.Equalf(t, http.StatusForbidden, rr.Code, "%s %s", tc.method, tc.path)
		assert.Containsf(t, rr.Body.String(), "user account", "%s %s should say why", tc.method, tc.path)
	}
}
