package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/connections"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
)

// A token endpoint standing in for a provider, so the manager can be driven end
// to end without the internet.
func fakeProviderToken(t *testing.T, body map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The point of the reshape: two connections of the same provider, told apart by
// slug, each handing back its own credential.
func TestTokenForResolvesBySlug(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	work := mustConnect(t, s, u.ID, "work-cal", "imap", map[string]string{"host": "h1", "username": "work@x", "password": "pw-work"})
	personal := mustConnect(t, s, u.ID, "personal-cal", "imap", map[string]string{"host": "h2", "username": "home@x", "password": "pw-home"})
	require.NoError(t, s.apps.Store().GrantConnection("a1", work.ID))
	require.NoError(t, s.apps.Store().GrantConnection("a1", personal.ID))

	a, err := s.apps.App("dash")
	require.NoError(t, err)

	got, err := s.connections.tokenFor(context.Background(), a, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "pw-work", got.AccessToken)
	assert.Contains(t, got.Meta, "work@x")

	got, err = s.connections.tokenFor(context.Background(), a, "personal-cal")
	require.NoError(t, err)
	assert.Equal(t, "pw-home", got.AccessToken, "the other one, not the first")
}

// An app that was never granted a connection must not be able to name it, and
// must be told which of the two things is missing.
func TestTokenForRefusesUngrantedAndUnknown(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	mustConnect(t, s, u.ID, "work-cal", "imap", map[string]string{"host": "h", "username": "w", "password": "pw"})
	a, err := s.apps.App("dash")
	require.NoError(t, err)

	_, err = s.connections.tokenFor(context.Background(), a, "work-cal")
	assert.ErrorIs(t, err, errNotGranted, "connected, but this app was not given it")

	_, err = s.connections.tokenFor(context.Background(), a, "no-such-slug")
	assert.ErrorIs(t, err, errNotConnected, "nothing by that name")
}

// One owner's slug is not another's: an app can only ever reach connections
// belonging to the person who owns it.
func TestAnAppNeverReachesAnotherOwnersConnection(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	mine := newActiveTestUser(t, s, "me@example.com")
	theirs := newActiveTestUser(t, s, "them@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "mine", Port: 10000, Host: store.HostLocal, OwnerID: mine.ID}))

	// Same slug, different owners, different secrets
	yours := mustConnect(t, s, theirs.ID, "cal", "imap", map[string]string{"host": "h", "username": "them", "password": "THEIRS"})
	mustConnect(t, s, mine.ID, "cal", "imap", map[string]string{"host": "h", "username": "me", "password": "MINE"})

	a, err := s.apps.App("mine")
	require.NoError(t, err)

	// Holding a grant on the OTHER owner's connection reaches nothing: the slug
	// resolves in the app owner's namespace, and that one was never granted.
	require.NoError(t, s.apps.Store().GrantConnection("a1", yours.ID))
	_, err = s.connections.tokenFor(context.Background(), a, "cal")
	assert.ErrorIs(t, err, errNotGranted, "a foreign grant is not a way in")

	// Granting my own makes my own reachable, and never theirs
	mineConn, err := s.apps.Store().ConnectionBySlug(mine.ID, "cal")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().GrantConnection("a1", mineConn.ID))
	got, err := s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	assert.Equal(t, "MINE", got.AccessToken, "resolved within the app owner's own namespace")
}

// The secret in the database is ciphertext; only the manager can open it.
func TestStoredSecretsAreSealed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	c := mustConnect(t, s, u.ID, "mail", "imap", map[string]string{"host": "h", "username": "u", "password": "hunter2"})

	row, err := s.apps.Store().Connection(c.ID)
	require.NoError(t, err)
	assert.NotContains(t, row.Secret, "hunter2", "the database never holds a usable credential")
	opened, err := connections.Open(s.connections.key, row.Secret)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", opened)
}

// A provider with no OAuth client configured is not offered at all.
func TestOnlyConfiguredProvidersAreOffered(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	byName := map[string]bool{}
	for _, p := range s.connections.offered() {
		byName[p.Name] = true
	}
	assert.True(t, byName["imap"], "a pasted credential needs no client")
	assert.True(t, byName["generic"], "and neither does the escape hatch")
	assert.False(t, byName["slack"], "no Slack client configured in tests")

	// Configure one and it appears, without a restart
	s.config.ConnectionClients = map[string]controlconf.OAuthClient{"slack": {ClientID: "id", ClientSecret: "sec"}}
	byName = map[string]bool{}
	for _, p := range s.connections.offered() {
		byName[p.Name] = true
	}
	assert.True(t, byName["slack"], "configuring a client offers the provider")
}

// A long-lived provider (Slack, GitHub) hands its stored token straight back.
func TestALongLivedConnectionReturnsItsStoredToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	sealed, err := connections.Seal(s.connections.key, "xoxb-stored")
	require.NoError(t, err)
	c := &store.Connection{UserID: u.ID, Slug: "work-slack", Provider: "slack", Kind: store.ConnectionOAuth, Secret: sealed}
	require.NoError(t, s.apps.Store().AddConnection(c))
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	got, err := s.connections.tokenFor(context.Background(), a, "work-slack")
	require.NoError(t, err)
	assert.Equal(t, "xoxb-stored", got.AccessToken)
	assert.True(t, got.ExpiresAt.IsZero(), "nothing expires, so nothing is promised")
}

// mustConnect stores a static connection the way the handler would.
func mustConnect(t *testing.T, s *Server, userID, slug, provider string, values map[string]string) *store.Connection {
	t.Helper()
	p, ok := connections.Lookup(provider)
	require.True(t, ok)
	c, err := s.connections.saveStatic(userID, slug, slug, p, values)
	require.NoError(t, err)
	return c
}
