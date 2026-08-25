package control

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
)

// The headline claim: a USER can register their own OAuth app with a vendor and
// use it here, with no admin involved. Nothing about OAuth requires the client
// to belong to the instance -- only the callback URL does, which is why the API
// hands it back rather than making people work it out.
func TestAUserDefinesTheirOwnProviderAndConnectsWithIt(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	body := `{"name":"acme","label":"Acme","scopes":["read"],"client_id":"my-own-client",` +
		`"client_secret":"my-own-secret","auth_url":"` + f.URL + `/authorize","token_url":"` + f.URL + `/token"}`
	rr := request(t, s.API(), "POST", "/api/providers", body, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var created apiProviderDefResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "personal", created.Scope)
	assert.True(t, created.Editable)
	assert.True(t, created.HasSecret)
	assert.NotContains(t, rr.Body.String(), "my-own-secret", "the client secret never comes back out")
	assert.Contains(t, created.RedirectURI, "/auth/callback",
		"the one thing nobody can work out themselves is handed to them")

	// It is offered in THEIR Add menu.
	list := listConnections(t, s, token, "?kind=oauth")
	var offered []string
	for _, p := range list.Providers {
		offered = append(offered, p.Name)
		if p.Name == "acme" {
			assert.True(t, p.Personal, "and badged as theirs")
		}
	}
	assert.Contains(t, offered, "acme")

	// Connecting it through the HANDLER, which is what the UI does. The
	// direct-call test below passed while this was broken: available() only
	// looked for a client in control.yml, so a personal provider was offered in
	// the menu and then refused on click.
	start := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"acme","slug":"work","label":"Work"}`, token)
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	assert.Contains(t, start.Body.String(), f.URL+"/authorize")
	assert.Contains(t, start.Body.String(), "my-own-client", "and with THEIR client id")

	// And the whole credential path works with THEIR client.
	p, ok := s.connections.providerFor(u.ID, "acme")
	require.True(t, ok)
	id, secret := s.connections.clientForUser(u.ID, "acme")
	assert.Equal(t, "my-own-client", id)
	assert.Equal(t, "my-own-secret", secret, "unsealed from the row, not from control.yml")

	code := consentCode(t, p, id)
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

// One person's provider is invisible to everybody else. It is their client,
// their secret and their account at the vendor.
func TestAPersonalProviderIsInvisibleToOtherUsers(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	mine := newActiveTestUser(t, s, "mine@example.com")
	other := newActiveTestUser(t, s, "other@example.com")
	mineToken, _, err := s.users.CreateToken(mine.ID, "laptop")
	require.NoError(t, err)
	otherToken, _, err := s.users.CreateToken(other.ID, "laptop")
	require.NoError(t, err)

	require.Equal(t, http.StatusCreated, request(t, s.API(), "POST", "/api/providers",
		`{"name":"acme","label":"Acme","client_id":"c","client_secret":"s","auth_url":"https://a/x","token_url":"https://a/t"}`,
		mineToken).Code)

	_, ok := s.connections.providerFor(other.ID, "acme")
	assert.False(t, ok, "somebody else's provider does not resolve")

	rr := request(t, s.API(), "DELETE", "/api/providers/acme", "", otherToken)
	assert.Equal(t, http.StatusNotFound, rr.Code, "and cannot be removed by them")

	// Which means the name is free for THEM to use for something else.
	assert.Equal(t, http.StatusCreated, request(t, s.API(), "POST", "/api/providers",
		`{"name":"acme","label":"Their Acme","client_id":"c2","client_secret":"s2","auth_url":"https://b/x","token_url":"https://b/t"}`,
		otherToken).Code)
	theirs, ok := s.connections.providerFor(other.ID, "acme")
	require.True(t, ok)
	assert.Equal(t, "Their Acme", theirs.Label)
}

// A user must not be able to redefine what a name means for their own apps if
// hostit or the operator already defines it -- "github" has to keep meaning
// GitHub.
func TestAPersonalProviderCannotShadowABuiltinOrTheOperators(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.ConnectionClients = map[string]controlconf.OAuthClient{"acme": {
		ClientID: "id", ClientSecret: "secret", Label: "Operator Acme",
		AuthURL: "https://a/x", TokenURL: "https://a/t",
	}}
	require.NoError(t, s.connections.loadCustomProviders(s.config))
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	for _, name := range []string{"github", "acme"} {
		rr := request(t, s.API(), "POST", "/api/providers",
			`{"name":"`+name+`","label":"Mine","client_id":"c","client_secret":"s","auth_url":"https://x/a","token_url":"https://x/t"}`,
			token)
		assert.Equal(t, http.StatusBadRequest, rr.Code, name)
	}
}

// Defining one for the whole instance is an admin act: it changes what a name
// means for everybody.
func TestOnlyAnAdminDefinesAnInstanceProvider(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	body := `{"name":"acme","label":"Acme","scope":"instance","client_id":"c","client_secret":"s","auth_url":"https://a/x","token_url":"https://a/t"}`
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/api/providers", body, token).Code)

	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))
	adminToken, _, err := s.users.CreateToken(admin.ID, "laptop")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, request(t, s.API(), "POST", "/api/providers", body, adminToken).Code)

	// And now EVERY user sees it, including the one refused a moment ago.
	p, ok := s.connections.providerFor(u.ID, "acme")
	require.True(t, ok)
	assert.Equal(t, "Acme", p.Label)
}

// Named MCP servers: the operator lists one so a user picks a name rather than
// remembering a URL. Both sources reach the same list.
func TestNamedMCPServersComeFromConfigAndFromAdmins(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.MCPServers = map[string]controlconf.MCPServer{
		"deepwiki": {Label: "DeepWiki", URL: "https://mcp.deepwiki.com/mcp", Help: "Ask about a repo"},
	}
	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))
	adminToken, _, err := s.users.CreateToken(admin.ID, "laptop")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/providers",
		`{"name":"linear","label":"Linear","kind":"mcp","scope":"instance","url":"https://mcp.linear.app/mcp"}`,
		adminToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	// And a user's own, alongside.
	require.Equal(t, http.StatusCreated, request(t, s.API(), "POST", "/api/providers",
		`{"name":"mine","label":"My server","kind":"mcp","url":"https://mcp.example.com/mcp"}`,
		token).Code)

	list := listConnections(t, s, token, "?kind=mcp")
	labels := map[string]bool{}
	for _, srv := range list.MCPServers {
		labels[srv.Label] = true
	}
	assert.True(t, labels["DeepWiki"], "from control.yml")
	assert.True(t, labels["Linear"], "from an admin")
	assert.True(t, labels["My server"], "and the user's own")
}

// consentCode walks a fake provider's consent and returns the code, without
// following the redirect to a callback hostname that does not exist.
func consentCode(t *testing.T, p providerWithAuthURL, clientID string) string {
	t.Helper()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noFollow.Get(p.AuthCodeURL(clientID, "https://hostit.example/auth/callback", "state-1"))
	require.NoError(t, err)
	defer res.Body.Close()
	back, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	code := back.Query().Get("code")
	require.NotEmpty(t, code)
	return code
}

type providerWithAuthURL interface {
	AuthCodeURL(clientID, redirectURL, state string) string
}
