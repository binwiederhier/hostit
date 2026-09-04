package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/http/outbound"
	"heckel.io/hostit/store"
)

// fakeMCP is a whole MCP server in one handler: the JSON-RPC endpoint, the
// RFC 9728 metadata that points at its authorization server, and that
// authorization server itself. One httptest server, because a real MCP host
// usually IS all three and the interesting part is hostit walking between them.
type fakeMCP struct {
	*httptest.Server
	needsAuth  bool
	noCIMD     bool           // an authorization server that will not take a URL as a client id
	calls      []string       // tool names actually invoked
	authorized []string       // bearer tokens seen on the MCP endpoint
	authQuery  url.Values     // the consent request, for asserting PKCE/resource
	tokenForms []url.Values   // the token requests
	issued     map[string]any // codes handed out
}

func newFakeMCP(t *testing.T, needsAuth bool) *fakeMCP {
	t.Helper()
	f := &fakeMCP{needsAuth: needsAuth, issued: map[string]any{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if f.needsAuth && bearer != "access-1" {
			w.Header().Set("WWW-Authenticate",
				`Bearer resource_metadata="`+f.URL+`/.well-known/oauth-protected-resource", scope="tools"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if bearer != "" {
			f.authorized = append(f.authorized, bearer)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"issues","version":"1"},"capabilities":{"tools":{}}}`
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = `{"tools":[{"name":"list_issues","description":"List issues","inputSchema":{"type":"object","properties":{"team":{"type":"string"}}}}]}`
		case "tools/call":
			f.calls = append(f.calls, req.Params.Name)
			result = `{"content":[{"type":"text","text":"issue: ` + req.Params.Name + `"}]}`
		default:
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":`+result+`}`)
	})

	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"resource":              f.URL + "/mcp",
			"authorization_servers": []string{f.URL},
			"scopes_supported":      []string{"tools"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"client_id_metadata_document_supported": !f.noCIMD,
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	// The consent screen approves immediately and bounces back with a code,
	// which is the only part hostit is on the hook for.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.authQuery = r.URL.Query()
		back := f.authQuery.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(f.authQuery.Get("state"))
		http.Redirect(w, r, back, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		f.tokenForms = append(f.tokenForms, form)
		writeTestJSON(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// An MCP server that wants nothing is the simple case: paste the URL, hostit
// asks it what it can do, and the tools are there to grant.
func TestAnOpenMCPServerIsConnectedByPastingItsURL(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	f := newFakeMCP(t, false)

	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"mcp","slug":"issues","label":"Issues","values":{"url":"`+f.URL+`/mcp"}}`, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var got apiConnectionResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "mcp", got.Kind)
	assert.Equal(t, f.URL+"/mcp", got.URL)
	require.Len(t, got.Tools, 1, "the tools are discovered at connect time, not configured")
	assert.Equal(t, "list_issues", got.Tools[0].Name)
	assert.Equal(t, "List issues", got.Tools[0].Description)
}

// The authenticated case, walked end to end: hostit is refused, finds the
// authorization server from the challenge, sends the owner to consent with
// PKCE and a resource indicator, and stores what comes back.
func TestAnAuthenticatedMCPServerIsConnectedThroughConsent(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	f := newFakeMCP(t, true)

	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"mcp","slug":"issues","label":"Issues","values":{"url":"`+f.URL+`/mcp"}}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Contains(t, started.RedirectURL, f.URL+"/authorize")

	consent, err := url.Parse(started.RedirectURL)
	require.NoError(t, err)
	q := consent.Query()
	assert.Equal(t, "S256", q.Get("code_challenge_method"), "hostit has no client secret here; PKCE is what stands in")
	assert.NotEmpty(t, q.Get("code_challenge"))
	assert.Equal(t, f.URL+"/mcp", q.Get("resource"), "the token must be useless against any other server")
	assert.Contains(t, q.Get("client_id"), "/.well-known/oauth-client",
		"hostit identifies itself by a URL the server fetches, so nothing is registered anywhere")

	// The owner approves; the provider bounces the code back to the callback.
	cb := callbackRequest(t, s, rr, q.Get("state"), u)
	require.Equal(t, http.StatusFound, cb.Code, cb.Body.String())

	require.Len(t, f.tokenForms, 1)
	assert.Equal(t, "authorization_code", f.tokenForms[0].Get("grant_type"))
	assert.NotEmpty(t, f.tokenForms[0].Get("code_verifier"))
	assert.Equal(t, f.URL+"/mcp", f.tokenForms[0].Get("resource"))

	list := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	var conns apiConnectionsResponse
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &conns))
	require.Len(t, conns.Connections, 1)
	assert.Equal(t, "issues", conns.Connections[0].Slug)
	require.Len(t, conns.Connections[0].Tools, 1, "the tool list is fetched once the token exists")
}

// What an app actually does with a granted MCP connection: ask what tools there
// are, then call one. The app never sees the token -- hostit makes the call.
func TestAGrantedAppListsAndCallsMCPTools(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	f := newFakeMCP(t, false)
	conn := mustConnectMCP(t, s, u.ID, "issues", f.URL+"/mcp")
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	tools := socketRequest(t, s, "GET", "/api/container/mcp/issues/tools")
	require.Equal(t, http.StatusOK, tools.Code, tools.Body.String())
	assert.Contains(t, tools.Body.String(), "list_issues")

	call := socketRequestBody(t, s, "POST", "/api/container/mcp/issues/call", `{"tool":"list_issues","arguments":{"team":"core"}}`)
	require.Equal(t, http.StatusOK, call.Code, call.Body.String())
	assert.Contains(t, call.Body.String(), "issue: list_issues")
	assert.Equal(t, []string{"list_issues"}, f.calls)
}

// A grant is the whole boundary. Without one the app is refused, and told which
// of the two things is missing.
func TestAnUngrantedAppCannotCallAnMCPTool(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	f := newFakeMCP(t, false)
	mustConnectMCP(t, s, u.ID, "issues", f.URL+"/mcp") // deliberately not granted

	call := socketRequestBody(t, s, "POST", "/api/container/mcp/issues/call", `{"tool":"list_issues"}`)
	assert.Equal(t, http.StatusForbidden, call.Code)
	assert.Empty(t, f.calls, "and the server was never contacted")

	missing := socketRequestBody(t, s, "POST", "/api/container/mcp/nothing/call", `{"tool":"x"}`)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

// The token endpoint is for credentials an app uses itself. An MCP token is not
// one of those: handing it over would let the app call the server directly,
// with the whole scope, bypassing the grant it was given.
func TestAnAppCannotFetchAnMCPConnectionsToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	f := newFakeMCP(t, false)
	conn := mustConnectMCP(t, s, u.ID, "issues", f.URL+"/mcp")
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	// 404, not 400: the request was perfectly well formed, and what it asked
	// for genuinely does not exist for this member. A 400 would tell an app to
	// go and look for a mistake it did not make.
	rr := socketRequest(t, s, "GET", "/api/container/connections/issues/token")
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "mcp")
	assert.Contains(t, rr.Body.String(), "/call", "and it says where the thing it CAN do lives")
}

// The client metadata document has to be readable by a server hostit has never
// spoken to, so it is public and unauthenticated on purpose.
func TestTheClientMetadataDocumentIsServedPublicly(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	rr := request(t, s.API(), "GET", "/.well-known/oauth-client", "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &doc))
	assert.Equal(t, "none", doc["token_endpoint_auth_method"])
	assert.Contains(t, doc["redirect_uris"], s.config.RedirectURL("apps.example.com"))
}

// mustConnectMCP attaches an MCP connection directly, for tests about what
// happens AFTER connecting.
func mustConnectMCP(t *testing.T, s *Server, userID, slug, serverURL string) *store.Connection {
	t.Helper()
	// A server that wants no authorization connects outright; one that does comes
	// back as errMCPNeedsConsent, which this helper is deliberately not for.
	conn, err := s.connections.addMCP(t.Context(), userID, slug, slug, serverURL)
	require.NoError(t, err)
	return conn
}

// callbackRequest replays the provider's redirect back to hostit, carrying the
// cookies the consent request set and the caller's session.
func callbackRequest(t *testing.T, s *Server, started *httptest.ResponseRecorder, state string, u *store.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/callback?code=the-code&state="+url.QueryEscape(state), nil)
	for _, ck := range started.Result().Cookies() {
		req.AddCookie(ck)
	}
	session, err := s.sessions.encode(u.ID)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: session})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	return rr
}

func socketRequestBody(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	return w
}

// A server whose authorization server accepts neither a URL client id nor
// registration cannot be connected. That has to fail HERE, with the reason,
// rather than sending the owner to a consent screen that refuses them.
func TestAServerHostitCannotIntroduceItselfToIsRefusedWithAReason(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	f := newFakeMCP(t, true)
	f.noCIMD = true

	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"mcp","slug":"issues","values":{"url":"`+f.URL+`/mcp"}}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "registration")
	assert.Empty(t, f.tokenForms, "and nothing was exchanged with it")
}

// The mirror of the token case, and it must answer the same way: a credential
// has no tools sub-resource, exactly as an MCP server has no token one. Two
// spellings of "that does not exist here" should not be two status codes.
func TestAskingForToolsOnACredentialIsAlsoANotFound(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	mustConnect(t, s, u.ID, "a-key", "generic", map[string]string{"secret": "x"})

	rr := request(t, s.API(), "GET", "/api/connections/a-key/mcp/tools", "", token)
	assert.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

// A user-supplied MCP URL is a request hostit makes from inside its own
// network. Pointed at loopback it would reach control's own API and every
// service bound there; pointed at 169.254.169.254 it would read the cloud
// metadata service, which on most providers is unauthenticated and full of
// credentials.
func TestAnMCPServerOnAnInternalAddressIsRefused(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.connections.client = outbound.NewClient(5*time.Second, nil) // the guard, as shipped
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	// A real listener on loopback, so a failure here means it was REACHED
	// rather than merely unreachable.
	reached := false
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeTestJSON(w, map[string]any{"secret": "internal"})
	}))
	t.Cleanup(internal.Close)

	for _, target := range []string{internal.URL + "/mcp", "http://169.254.169.254/latest/meta-data/"} {
		rr := request(t, s.API(), "POST", "/api/connections",
			`{"provider":"mcp","slug":"probe","values":{"url":"`+target+`"}}`, token)
		assert.NotEqual(t, http.StatusCreated, rr.Code, target)
		assert.Contains(t, rr.Body.String(), "not reachable", target)
	}
	assert.False(t, reached, "hostit connected to a loopback service on a user's say-so")
}
