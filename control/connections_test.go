package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/connections"
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
	opened, err := connections.Open(s.connections.key, row.Secret, connections.Binding(row.UserID, row.ID))
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
	assert.False(t, byName["slack-bot"], "no Slack client configured in tests")

	// Configure one and it appears, without a restart
	s.config.ConnectionClients = map[string]config.OAuthClient{"slack-bot": {ClientID: "id", ClientSecret: "sec"}}
	byName = map[string]bool{}
	for _, p := range s.connections.offered() {
		byName[p.Name] = true
	}
	assert.True(t, byName["slack-bot"], "configuring a client offers the provider")
}

// A long-lived provider (Slack, GitHub) hands its stored token straight back.
func TestALongLivedConnectionReturnsItsStoredToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	c := &store.Connection{ID: store.NewConnectionID(), UserID: u.ID, Slug: "work-slack", Provider: "slack-bot", Kind: store.ConnectionOAuth}
	sealed, err := connections.Seal(s.connections.key, "xoxb-stored", connections.Binding(c.UserID, c.ID))
	require.NoError(t, err)
	c.Secret = sealed
	require.NoError(t, s.apps.Store().AddConnection(c))
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	got, err := s.connections.tokenFor(context.Background(), a, "work-slack")
	require.NoError(t, err)
	assert.Equal(t, "xoxb-stored", got.AccessToken)
	assert.Nil(t, got.ExpiresAt, "nothing expires, so nothing is promised")
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

// The attack this binding exists to stop, kept as a test because it WORKED
// before: copy one person's sealed credential into a row you control, and read
// their secret out of your own app.
//
// It needs database write access, which on a single box implies the key file
// beside it -- so this is defence in depth, not the last line. It is worth
// having because the cheap failures are the likely ones: a migration that
// mixes rows, a restore from the wrong backup, a future query with a wrong
// WHERE clause.
func TestAStolenCiphertextDoesNotOpenInAnotherRow(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	victim := newActiveTestUser(t, s, "victim@example.com")
	attacker := newActiveTestUser(t, s, "attacker@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "evil", Port: 10000, Host: store.HostLocal, OwnerID: attacker.ID}))

	victimConn := mustConnect(t, s, victim.ID, "mail", "generic", map[string]string{"secret": "VICTIM-SECRET"})
	attackerConn := mustConnect(t, s, attacker.ID, "mine", "generic", map[string]string{"secret": "attacker-own"})

	// Straight into the database, bypassing every check
	require.NoError(t, s.apps.Store().UpdateConnectionSecret(attackerConn.ID, victimConn.Secret, "", ""))
	require.NoError(t, s.apps.Store().GrantConnection("a1", attackerConn.ID))

	a, err := s.apps.App("evil")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(context.Background(), a, "mine")
	require.Error(t, err, "a credential sealed for another row must not open here")
	assert.NotContains(t, err.Error(), "VICTIM-SECRET")
}

// If the credential key leaks there has to be a way out that is not "ask every
// user to re-authorise every account". Rotation re-seals every stored
// credential under a fresh key, in one pass, leaving slugs and grants alone.
func TestRotateTheCredentialKey(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	other := newActiveTestUser(t, s, "other@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	mine := mustConnect(t, s, u.ID, "mail", "generic", map[string]string{"secret": "MINE"})
	theirs := mustConnect(t, s, other.ID, "mail", "generic", map[string]string{"secret": "THEIRS"})
	require.NoError(t, s.apps.Store().GrantConnection("a1", mine.ID))
	before := mine.Secret

	old := s.connections.key
	n, err := s.connections.RotateKey()
	require.NoError(t, err)
	assert.Equal(t, 2, n, "every credential was re-sealed")
	assert.NotEqual(t, old, s.connections.key, "under a new key")

	// The old key no longer opens anything
	row, err := s.apps.Store().Connection(mine.ID)
	require.NoError(t, err)
	assert.NotEqual(t, before, row.Secret, "the stored ciphertext changed")
	_, err = connections.Open(old, row.Secret, connections.Binding(row.UserID, row.ID))
	assert.Error(t, err, "the leaked key is now useless")

	// And everything still works, for both owners, with grants intact
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	tok, err := s.connections.tokenFor(context.Background(), a, "mail")
	require.NoError(t, err)
	assert.Equal(t, "MINE", tok.AccessToken)

	got, err := connections.Open(s.connections.key, mustRow(t, s, theirs.ID).Secret, connections.Binding(other.ID, theirs.ID))
	require.NoError(t, err)
	assert.Equal(t, "THEIRS", got, "the other owner's is re-sealed too, still bound to its own row")
}

func mustRow(t *testing.T, s *Server, id string) *store.Connection {
	t.Helper()
	c, err := s.apps.Store().Connection(id)
	require.NoError(t, err)
	return c
}

// An operator's own provider, written in control.yml, must be indistinguishable
// from a built-in everywhere it matters: offered in the menu, connectable,
// refreshable. It is registered per INSTANCE rather than into the package's
// global catalog, so two servers in one test binary cannot see each other's.
func TestACustomProviderFromConfigIsOfferedAndConnectable(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "acme-secret",
		Label:    "Acme",
		Scopes:   []string{"read"},
		AuthURL:  f.URL + "/authorize",
		TokenURL: f.URL + "/token",
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))

	p, ok := s.connections.lookup("acme")
	require.True(t, ok, "an operator's provider is a provider")
	assert.Equal(t, "Acme", p.Label)
	assert.True(t, p.Custom)

	var offered []string
	for _, o := range s.connections.offered() {
		offered = append(offered, o.Name)
	}
	assert.Contains(t, offered, "acme")

	// And the whole credential path works, which is the actual claim.
	u := newActiveTestUser(t, s, "owner@example.com")
	// Walk the consent the way the fake expects: /authorize mints the code and
	// bounces it back. The redirect is NOT followed -- it points at a hostname
	// that does not exist, which is the whole point of it being a callback.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noFollow.Get(p.AuthCodeURL("acme-id", "https://hostit.example/auth/callback", "state-1"))
	require.NoError(t, err)
	defer res.Body.Close()
	back, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	code := back.Query().Get("code")
	require.NotEmpty(t, code)

	conn, err := s.connections.saveOAuth(t.Context(), u.ID, "acme", "Acme", p, code, "https://hostit.example/auth/callback", "")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	tok, err := s.connections.tokenFor(t.Context(), a, "acme")
	require.NoError(t, err)
	assert.NotEmpty(t, tok.AccessToken)
}

// Editing permissions reconnects with a new scope set; the stored scopes must
// FOLLOW what the owner just chose, not keep the old grant. (Regression: a
// re-consent reused c.Scopes, so unticking a box and reconnecting left the
// connection -- and the edit dialog's checkboxes -- unchanged.)
func TestReconnectStoresTheNewScopes(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "acme-secret", Label: "Acme",
		Scopes: []string{"read"}, AuthURL: f.URL + "/authorize", TokenURL: f.URL + "/token",
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	p, ok := s.connections.lookup("acme")
	require.True(t, ok)
	u := newActiveTestUser(t, s, "owner@example.com")
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	codeFor := func() string {
		res, err := noFollow.Get(p.AuthCodeURL("acme-id", "https://hostit.example/auth/callback", "st"))
		require.NoError(t, err)
		defer res.Body.Close()
		back, err := url.Parse(res.Header.Get("Location"))
		require.NoError(t, err)
		return back.Query().Get("code")
	}
	// Created with a broad grant.
	conn, err := s.connections.saveOAuth(t.Context(), u.ID, "acme", "Acme", p, codeFor(), "https://hostit.example/auth/callback", "read write extra")
	require.NoError(t, err)
	require.Equal(t, "read write extra", conn.Scopes)

	// Reconnect with a REDUCED set -> the stored scopes follow it.
	require.NoError(t, s.connections.reconnect(t.Context(), conn, p, codeFor(), "https://hostit.example/auth/callback", "read"))
	got, err := s.apps.Store().ConnectionBySlug(u.ID, "acme")
	require.NoError(t, err)
	assert.Equal(t, "read", got.Scopes, "reconnect must persist the new grant, not the old one")

	// An empty scopes string (a lost round-trip cookie) must NOT wipe the grant.
	require.NoError(t, s.connections.reconnect(t.Context(), got, p, codeFor(), "https://hostit.example/auth/callback", ""))
	got2, err := s.apps.Store().ConnectionBySlug(u.ID, "acme")
	require.NoError(t, err)
	assert.Equal(t, "read", got2.Scopes, "a lost cookie keeps the old grant rather than wiping it")
}

// A custom entry hostit cannot use must stop the server at load, where the
// operator is looking at the file they just edited.
func TestABrokenCustomProviderRefusesToLoad(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "acme-secret", Label: "Acme",
	}}
	err := s.connections.loadCustomProviders(s.config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-url")
}

// A bare client-id/client-secret pair under a name hostit ships is the ordinary
// case and must NOT be mistaken for a custom provider.
func TestConfiguringABuiltinIsNotACustomProvider(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"github": {ClientID: "id", ClientSecret: "secret"}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))

	p, ok := s.connections.lookup("github")
	require.True(t, ok)
	assert.False(t, p.Custom, "it is hostit's github, with the operator's client")
	assert.Equal(t, "https://github.com/login/oauth/authorize", p.AuthURL)
}

// An operator can REPLACE a built-in's permission checkboxes from control.yml
// (e.g. to offer a write scope) without hostit shipping a code change.
func TestOperatorScopeOptionsReplaceBuiltinCheckboxes(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"slack-user": {
		ClientID: "id", ClientSecret: "secret", // a client-only entry, NOT a custom provider
		ScopeOptions: []config.OAuthScopeOption{
			{Key: "read", Label: "Read channels", Scopes: []string{"channels:read", "channels:history"}, Default: true},
			{Key: "post", Label: "Post messages", Scopes: []string{"chat:write"}},
		},
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))

	p, ok := s.connections.lookup("slack-user")
	require.True(t, ok)
	assert.False(t, p.Custom, "still hostit's built-in, just with the operator's options")
	var keys []string
	for _, o := range p.ScopeOptions {
		keys = append(keys, o.Key)
	}
	assert.Equal(t, []string{"read", "post"}, keys, "operator options REPLACE the built-in defaults")
	// And the replacement resolves correctly (write is now grantable).
	scopes, err := p.ResolveScopes([]string{"read", "post"})
	require.NoError(t, err)
	assert.Contains(t, scopes, "chat:write")
}

// The OFFERED list (what the UI renders as checkboxes) must reflect the override
// too, not just connect-time resolution -- otherwise the UI shows the default
// boxes while a connect resolves against different ones.
func TestOperatorScopeOptionsShowInOfferedList(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"slack-user": {
		ClientID: "id", ClientSecret: "secret",
		ScopeOptions: []config.OAuthScopeOption{
			{Key: "read", Label: "Read channels", Scopes: []string{"channels:read"}, Default: true},
			{Key: "post", Label: "Post messages", Scopes: []string{"chat:write"}},
		},
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))

	var found *connections.Provider
	for _, p := range s.connections.offeredFor("some-user") {
		if p.Name == "slack-user" {
			pp := p
			found = &pp
			break
		}
	}
	require.NotNil(t, found, "slack-user is offered")
	var keys []string
	for _, o := range found.ScopeOptions {
		keys = append(keys, o.Key)
	}
	assert.Equal(t, []string{"read", "post"}, keys, "the offered list shows the operator's options, not the built-in defaults")
}

// A custom provider defines its permission checkboxes entirely in config.
func TestCustomProviderCarriesScopeOptions(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme-sso": {
		ClientID: "id", ClientSecret: "secret", Label: "Acme SSO",
		AuthURL: "https://sso.acme.example/authorize", TokenURL: "https://sso.acme.example/token",
		UserToken: true,
		ScopeOptions: []config.OAuthScopeOption{
			{Key: "profile", Label: "Profile", Scopes: []string{"profile"}, Default: true},
			{Key: "projects", Label: "Projects", Scopes: []string{"projects:read"}},
		},
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))

	p, ok := s.connections.lookup("acme-sso")
	require.True(t, ok)
	assert.True(t, p.Custom)
	assert.True(t, p.UserToken, "a custom provider can request a user token (Slack-style)")
	require.Len(t, p.ScopeOptions, 2)
	assert.Equal(t, "profile", p.ScopeOptions[0].Key)
	assert.Equal(t, []string{"projects:read"}, p.ScopeOptions[1].Scopes)
}

// Removing a connection revokes its token at the provider, so a discarded
// credential does not stay valid out there. And when two connections share the
// same app (same token), removing one must NOT revoke -- it would break the other.
func TestDeleteRevokesTheTokenUnlessAppIsShared(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.longLived = true // Slack-like: the stored credential IS the access token
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "sec", Label: "Acme",
		AuthURL: f.URL + "/authorize", TokenURL: f.URL + "/token", RevokeURL: f.URL + "/revoke", LongLivedToken: true,
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	p, ok := s.connections.lookup("acme")
	require.True(t, ok)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	codeFor := func() string {
		res, err := noFollow.Get(p.AuthCodeURL("acme-id", "https://hostit.example/auth/callback", "st"))
		require.NoError(t, err)
		defer res.Body.Close()
		back, _ := url.Parse(res.Header.Get("Location"))
		return back.Query().Get("code")
	}
	c1, err := s.connections.saveOAuth(t.Context(), u.ID, "one", "One", p, codeFor(), "https://hostit.example/auth/callback", "read")
	require.NoError(t, err)
	stored, err := s.connections.open(c1)
	require.NoError(t, err)

	// Delete the only connection on this app -> its token is revoked.
	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/connections/one", "", token).Code)
	assert.Equal(t, []string{stored}, f.revokedTokens(), "the removed connection's token is revoked")

	// Now two connections on the SAME app: deleting one must not revoke (shared token).
	c2, err := s.connections.saveOAuth(t.Context(), u.ID, "two", "Two", p, codeFor(), "https://hostit.example/auth/callback", "read")
	require.NoError(t, err)
	_, err = s.connections.saveOAuth(t.Context(), u.ID, "three", "Three", p, codeFor(), "https://hostit.example/auth/callback", "read")
	require.NoError(t, err)
	before := len(f.revokedTokens())
	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/connections/"+c2.Slug, "", token).Code)
	assert.Len(t, f.revokedTokens(), before, "a shared app is not revoked when one of its connections is removed")
}

// Editing the granted scopes revokes the current token BEFORE the re-consent, so
// a provider that unions past grants (Slack) is deauthorized first and the fresh
// consent narrows -- the same reason remove+re-add narrows. An UNCHANGED
// reconnect keeps the token (a working credential is not thrown away for nothing).
func TestReconnectRevokesBeforeReconsentWhenScopesChange(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.longLived = true
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "sec", Label: "Acme",
		AuthURL: f.URL + "/authorize", TokenURL: f.URL + "/token", RevokeURL: f.URL + "/revoke",
		LongLivedToken: true,
		ScopeOptions: []config.OAuthScopeOption{
			{Key: "read", Label: "Read", Scopes: []string{"r"}, Default: true},
			{Key: "write", Label: "Write", Scopes: []string{"w"}},
		},
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	p, ok := s.connections.lookup("acme")
	require.True(t, ok)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	codeFor := func() string {
		res, err := noFollow.Get(p.AuthCodeURL("acme-id", "https://hostit.example/auth/callback", "st"))
		require.NoError(t, err)
		defer res.Body.Close()
		back, _ := url.Parse(res.Header.Get("Location"))
		return back.Query().Get("code")
	}
	conn, err := s.connections.saveOAuth(t.Context(), u.ID, "acme", "Acme", p, codeFor(), "https://hostit.example/auth/callback", "r")
	require.NoError(t, err)
	stored, err := s.connections.open(conn)
	require.NoError(t, err)

	// Unchanged reconnect: the token is kept.
	require.Equal(t, http.StatusOK, request(t, s.API(), "POST", "/api/connections/acme/reconnect", `{"scope_keys":["read"]}`, token).Code)
	assert.Empty(t, f.revokedTokens(), "an unchanged reconnect keeps the token")

	// Changed grant: the old token is revoked BEFORE re-consent.
	require.Equal(t, http.StatusOK, request(t, s.API(), "POST", "/api/connections/acme/reconnect", `{"scope_keys":["read","write"]}`, token).Code)
	assert.Equal(t, []string{stored}, f.revokedTokens(), "changing the grant revokes the old token before re-consent")
}
