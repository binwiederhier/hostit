package control

import (
	"encoding/json"
	"fmt"
	"heckel.io/hostit/control/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control/connections"
	"heckel.io/hostit/store"
)

// addCredential posts a pasted credential the way the UI does.
func addCredential(t *testing.T, s *Server, token, slug, label string) *apiConnectionResponse {
	t.Helper()
	body := fmt.Sprintf(`{"provider":"generic","slug":%q,"label":%q,"values":{"secret":"sk-%s"}}`, slug, label, slug)
	rr := request(t, s.API(), "POST", "/api/connections", body, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var out apiConnectionResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return &out
}

func TestConnectionsCrud(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Empty(t, list.Connections)
	assert.NotEmpty(t, list.Providers, "the instance says what it can connect")

	c := addCredential(t, s, token, "openai", "OpenAI key")
	assert.Equal(t, "openai", c.Slug)
	assert.Equal(t, "generic", c.Provider)
	assert.Equal(t, "static", c.Kind)

	// The stored secret is never echoed back, on create or on list
	rr = request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), "sk-openai", "a stored secret is never readable through the API")

	// Rename
	rr = request(t, s.API(), "PUT", "/api/connections/openai", `{"slug":"openai-work","label":"Work key"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = request(t, s.API(), "GET", "/api/connections", "", token)
	assert.Contains(t, rr.Body.String(), "openai-work")

	rr = request(t, s.API(), "DELETE", "/api/connections/openai-work", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/connections/openai-work", "", token)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// The reshape, through the API: two of the same provider, told apart by slug.
func TestTwoConnectionsOfTheSameProvider(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	addCredential(t, s, token, "work-key", "Work")
	addCredential(t, s, token, "home-key", "Home")

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Connections, 2)

	// A slug cannot be reused by its owner
	body := `{"provider":"generic","slug":"work-key","label":"again","values":{"secret":"sk-x"}}`
	rr = request(t, s.API(), "POST", "/api/connections", body, token)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestConnectionSlugValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	for _, slug := range []string{"", "Has Spaces", "has/slash", "-leading", "trailing-", "x", "under_score"} {
		body := fmt.Sprintf(`{"provider":"generic","slug":%q,"values":{"secret":"sk-x"}}`, slug)
		rr := request(t, s.API(), "POST", "/api/connections", body, token)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "should reject slug %q", slug)
	}
	// Case is normalised rather than refused: a slug is an identifier, and
	// rejecting "Work-Cal" teaches nothing that lowercasing it does not.
	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"generic","slug":"Work-Cal","values":{"secret":"sk-x"}}`, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var made apiConnectionResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &made))
	assert.Equal(t, "work-cal", made.Slug)
	// A provider this instance cannot offer is refused rather than half-created
	rr = request(t, s.API(), "POST", "/api/connections", `{"provider":"slack-bot","slug":"work-slack"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "no Slack client is configured here")
}

// Connections belong to their owner and nobody else can see or touch them.
func TestConnectionsAreOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	mine := accountToken(t, s, newActiveTestUser(t, s, "me@example.com"))
	theirs := accountToken(t, s, newActiveTestUser(t, s, "them@example.com"))
	addCredential(t, s, mine, "secret-thing", "Mine")

	rr := request(t, s.API(), "GET", "/api/connections", "", theirs)
	require.Equal(t, http.StatusOK, rr.Code)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Empty(t, list.Connections, "another account sees none of mine")

	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "DELETE", "/api/connections/secret-thing", "", theirs).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/connections/secret-thing", `{"slug":"stolen"}`, theirs).Code)
}

// Granting is per app and per connection: an app gets the one it was given.
func TestAppGrants(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	addCredential(t, s, token, "work-key", "Work")
	addCredential(t, s, token, "home-key", "Home")

	rr := request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var view apiAppConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)
	assert.Len(t, view.Available, 2, "both of the owner's, offered to grant")

	rr = request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	require.Len(t, view.Granted, 1)
	assert.Equal(t, "work-key", view.Granted[0].Slug)

	rr = request(t, s.API(), "DELETE", "/api/apps/dash/connections/work-key", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)

	// You cannot grant an app a connection that is not yours
	other := accountToken(t, s, newActiveTestUser(t, s, "them@example.com"))
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", other).Code)
}

// Disconnecting cuts every app off, rather than leaving a grant pointing at
// nothing.
func TestDisconnectingRevokesEveryGrant(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	addCredential(t, s, token, "work-key", "Work")
	require.Equal(t, http.StatusOK, request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", token).Code)

	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/connections/work-key", "", token).Code)
	rr := request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	var view apiAppConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)
}

// A credential the owner typed wrongly is THEIR mistake to fix, so it must come
// back as a 400 with the reason. Before this these fell through to a 500, which
// says "hostit broke" about a pasted public key.
func TestBadCredentialValuesAreABadRequest(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	// A public key in the private key box: the mistake worth catching
	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"ssh-key","slug":"deploy-key","label":"Deploy","values":{"private-key":"ssh-ed25519 AAAAC3Nza me@laptop"}}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "PUBLIC half", "and says what is wrong")

	// A required field left empty
	rr = request(t, s.API(), "POST", "/api/connections",
		`{"provider":"imap","slug":"mail","label":"Mail","values":{"host":"imap.example.com:993"}}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "required")
}

// One collection with three kinds in it means a client that cares about one
// kind should not have to fetch and sift all three. The providers are filtered
// alongside, or an "MCP only" view still offers you a Postgres credential.
func TestConnectionsCanBeListedByKind(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	f := newFakeMCP(t, false)
	mustConnect(t, s, u.ID, "a-key", "generic", map[string]string{"secret": "x"})
	_, err = s.connections.addMCP(t.Context(), u.ID, "issues", "Issues", f.URL+"/mcp")
	require.NoError(t, err)

	all := listConnections(t, s, token, "")
	require.Len(t, all.Connections, 2)

	static := listConnections(t, s, token, "?kind=static")
	require.Len(t, static.Connections, 1)
	assert.Equal(t, "a-key", static.Connections[0].Slug)
	for _, p := range static.Providers {
		assert.Equal(t, "static", p.Kind, "an MCP provider has no business in a credentials-only view")
	}

	mcp := listConnections(t, s, token, "?kind=mcp")
	require.Len(t, mcp.Connections, 1)
	assert.Equal(t, "issues", mcp.Connections[0].Slug)

	oauth := listConnections(t, s, token, "?kind=oauth")
	assert.Empty(t, oauth.Connections)
}

// A typo must not silently answer with everything: a client asking for one kind
// and getting three would trust the answer and be wrong.
func TestAnUnknownKindIsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	rr := request(t, s.API(), "GET", "/api/connections?kind=mpc", "", token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "mpc")
}

func listConnections(t *testing.T, s *Server, token, query string) apiConnectionsResponse {
	t.Helper()
	rr := request(t, s.API(), "GET", "/api/connections"+query, "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out
}

// A pasted credential must be replaceable IN PLACE. Rotating an API key used to
// mean deleting the connection and adding it again, which silently dropped
// every grant with it -- the API always allowed the edit, the dialog just never
// asked for it.
func TestReplacingAPastedCredentialKeepsItsGrants(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }

	conn := mustConnect(t, s, u.ID, "a-key", "generic", map[string]string{"secret": "old-secret"})
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	rr := request(t, s.API(), "PUT", "/api/connections/a-key",
		`{"slug":"a-key","label":"Rotated","values":{"secret":"new-secret"}}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// The app sees the NEW secret, and never had to be re-granted.
	tok := socketRequest(t, s, "GET", "/api/container/connections/a-key/token")
	require.Equal(t, http.StatusOK, tok.Code, tok.Body.String())
	assert.Contains(t, tok.Body.String(), "new-secret")
	assert.NotContains(t, tok.Body.String(), "old-secret")

	n, err := s.apps.Store().CountGrants(conn.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the grant survived the rotation")
}

// Finishing a consent has to land where connections live. It landed on /profile,
// which has not held them since they moved to their own page.
func TestAFinishedConsentLandsOnTheConnectionsPage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "id", ClientSecret: "secret", Label: "Acme",
		AuthURL: f.URL + "/authorize", TokenURL: f.URL + "/token",
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	u := newActiveTestUser(t, s, "owner@example.com")

	p, ok := s.connections.lookup("acme")
	require.True(t, ok)
	code := consentCode(t, p, "id")

	// The state is echoed by the provider AND kept in a cookie; the callback
	// refuses unless they agree, which is what stops a code being planted.
	state := "conn:acme:work:nonce"
	req := httptest.NewRequest("GET", "/auth/callback?code="+code+"&state="+state, nil)
	session, err := s.sessions.encode(u.ID)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: session})
	req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: state})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)

	require.Equal(t, http.StatusFound, rr.Code, rr.Body.String())
	assert.Equal(t, "/connections", rr.Header().Get("Location"))
}

// The add dialog shows a one-line description of what a connection is; the long
// form (API docs for an app's assistant) is NOT offered to the dashboard.
func TestOfferedProvidersCarryTheShortDescriptionOnly(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme-desc": {
		ClientID: "id", ClientSecret: "sec", Label: "Acme",
		AuthURL: "https://a.example/x", TokenURL: "https://a.example/t",
		ShortDescription: "acme-short-line", LongDescription: "acme-long-api-docs",
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "acme-short-line", "the add dialog gets the short description")
	assert.NotContains(t, rr.Body.String(), "acme-long-api-docs", "the long API description is not offered to the dashboard")
}

// A granted app discovers both descriptions from its own connections endpoint:
// the short line and the long API docs its assistant reads.
func TestContainerConnectionsCarryBothDescriptions(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	connections.Register(connections.Provider{
		Name: "described-key", Label: "Described", Kind: connections.KindStatic,
		SecretField:      "secret",
		Fields:           []connections.Field{{Name: "secret", Label: "Secret", Secret: true}},
		ShortDescription: "described-short-line",
		LongDescription:  "described-long-api-docs",
	})
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	conn := mustConnect(t, s, u.ID, "mykey", "described-key", map[string]string{"secret": "x"})
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	rr := socketRequest(t, s, "GET", "/api/container/connections")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "described-short-line")
	assert.Contains(t, rr.Body.String(), "described-long-api-docs")
}

// allow-multiple false refuses a SECOND connection on the same OAuth app (the
// two would share one grant); the default allows it.
func TestDisallowMultipleRefusesASecondConnection(t *testing.T) {
	t.Parallel()
	no, yes := false, true
	run := func(allow *bool, wantSecond int) {
		s := newTestServer(t)
		s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
			ClientID: "acme-id", ClientSecret: "sec", Label: "Acme",
			AuthURL: "https://a.example/x", TokenURL: "https://a.example/t",
			AllowMultiple: allow,
		}}
		require.NoError(t, s.connections.loadCustomProviders(s.config))
		u := newActiveTestUser(t, s, fmt.Sprintf("owner-%p@example.com", allow))
		token, _, err := s.users.CreateToken(u.ID, "laptop")
		require.NoError(t, err)
		require.NoError(t, s.apps.Store().AddConnection(&store.Connection{
			ID: store.NewConnectionID(), UserID: u.ID, Slug: "acme-one", Label: "One",
			Provider: "acme", Kind: store.ConnectionOAuth, CreatedAt: time.Now(),
		}))
		rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"acme","slug":"acme-two"}`, token)
		assert.Equal(t, wantSecond, rr.Code, rr.Body.String())
	}
	run(&no, http.StatusConflict)
	run(&yes, http.StatusOK)
	run(nil, http.StatusOK) // unset == allow
}

// A provider that disallows multiple is dropped from the offered list once the
// owner has a connection on its app -- a second would only alias the first, so
// there is nothing to pick.
func TestDisallowMultipleProviderIsNotOfferedOnceConnected(t *testing.T) {
	t.Parallel()
	no := false
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "sec", Label: "Acme",
		AuthURL: "https://a.example/x", TokenURL: "https://a.example/t",
		AllowMultiple: &no,
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	offered := func() bool {
		rr := request(t, s.API(), "GET", "/api/connections", "", token)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out apiConnectionsResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		for _, p := range out.Providers {
			if p.Name == "acme" {
				return true
			}
		}
		return false
	}
	assert.True(t, offered(), "offered before any connection exists")

	require.NoError(t, s.apps.Store().AddConnection(&store.Connection{
		ID: store.NewConnectionID(), UserID: u.ID, Slug: "acme-one", Label: "One",
		Provider: "acme", Kind: store.ConnectionOAuth, CreatedAt: time.Now(),
	}))
	assert.False(t, offered(), "not offered once its app is connected")
}

// A connection carries its OWN scope-options, so the edit dialog can render the
// permission checkboxes even for a provider that dropped out of the offered list
// (allow-multiple off, already connected). Regression: filtering that list left
// the edit dialog with no provider to read scope-options from.
func TestConnectionViewCarriesScopeOptionsWhenProviderDisallowsMultiple(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	no := false
	s.config.ConnectionClients = map[string]config.OAuthClient{"acme": {
		ClientID: "acme-id", ClientSecret: "sec", Label: "Acme",
		AuthURL: "https://a.example/x", TokenURL: "https://a.example/t",
		AllowMultiple: &no,
		ScopeOptions: []config.OAuthScopeOption{
			{Key: "read", Label: "Read", Scopes: []string{"a:read"}, Default: true},
			{Key: "write", Label: "Write", Scopes: []string{"a:write"}},
		},
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddConnection(&store.Connection{
		ID: store.NewConnectionID(), UserID: u.ID, Slug: "my-acme", Label: "Mine",
		Provider: "acme", Kind: store.ConnectionOAuth, Scopes: "a:read", CreatedAt: time.Now(),
	}))

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	// The provider is NOT offered (allow-multiple off + already connected)...
	for _, p := range out.Providers {
		assert.NotEqual(t, "acme", p.Name, "a disallow-multiple provider drops out of the offered list once connected")
	}
	// ...but the connection still carries its scope-options for the edit dialog.
	require.Len(t, out.Connections, 1)
	require.Len(t, out.Connections[0].ScopeOptions, 2, "the edit dialog gets the checkboxes from the connection")
	assert.Equal(t, "read", out.Connections[0].ScopeOptions[0].Key)
	assert.Equal(t, []string{"read"}, out.Connections[0].ScopeKeys, "and which ones are ticked")
}
