package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	testToken     = "secr3t"
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
)

func TestAPIHealth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/v1/health", "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"healthy":true`)
}

func TestAPIUnauthorized(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, token := range []string{"", "wrong"} {
		rr := request(t, s.API(), "GET", "/v1/apps", "", token)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	}
}

func TestAPICreateApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body := fmt.Sprintf(`{"name":"blog","ssh_keys":["%s"]}`, testPublicKey)
	rr := request(t, s.API(), "POST", "/v1/apps", body, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.Name)
	assert.Equal(t, "https://blog.apps.example.com", resp.URL)
	assert.Equal(t, 10000, resp.Port)
	assert.Equal(t, "blog", resp.SSH.User)
	assert.Equal(t, "apps.example.com", resp.SSH.Host)
}

func TestAPICreateAppWithoutKeys(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// Creating an app must never mint an SSH key: a new user has nothing to do
	// with a private key, and the app is managed through the API
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	assert.NotContains(t, rr.Body.String(), "PRIVATE KEY")
	assert.NotContains(t, rr.Body.String(), "private_key")
	keys, err := s.apps.Store().AppKeys("blog")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestAPICreateAppInvalidName(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"NOPE!"}`, testToken)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAPICreateAppDuplicate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestAPIListGetDeleteApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "GET", "/v1/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
	rr = request(t, s.API(), "GET", "/v1/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/v1/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/v1/apps/blog", "", testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = request(t, s.API(), "DELETE", "/v1/apps/blog", "", testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAPIInviteUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/users", `{"email":"NewHire@allowed.example","role":"user"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "newhire@allowed.example", created.Email)
	assert.Equal(t, store.StatusActive, created.Status, "an invited user does not wait for approval")
	assert.Equal(t, store.RoleUser, created.Role)

	// They show up in the user list right away, before ever signing in
	rr = request(t, s.API(), "GET", "/v1/users", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var users []*apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	assert.Equal(t, "newhire@allowed.example", users[0].Email)

	// Duplicates and junk are refused with a reason, not a 500
	rr = request(t, s.API(), "POST", "/v1/users", `{"email":"newhire@allowed.example"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "POST", "/v1/users", `{"email":"not-an-email"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAPIAllowedDomains(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/v1/domains", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "[]", strings.TrimSpace(rr.Body.String()))

	// The admin types what they mean; hostit stores the bare domain
	rr = request(t, s.API(), "POST", "/v1/domains", `{"domain":"*@allowed.example"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "allowed.example", created.Domain)

	rr = request(t, s.API(), "GET", "/v1/domains", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var domains []*apiDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &domains))
	require.Len(t, domains, 1)
	assert.Equal(t, "allowed.example", domains[0].Domain)

	// And it works: the next sign-in from that domain needs no approval
	u, err := s.users.Login("newhire@allowed.example", "New Hire")
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, u.Status)

	rr = request(t, s.API(), "POST", "/v1/domains", `{"domain":"nope"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "DELETE", "/v1/domains/allowed.example", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/v1/domains/allowed.example", "", testToken)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAPIAppDescription(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, s.apps.WriteFile("blog", "hostit.yml", []byte("description: A tiny blog\nmode: static\n"), 0))

	// Both the list and the single app carry it, so the page can build the
	// prompt from whatever the app says it is
	rr = request(t, s.API(), "GET", "/v1/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "A tiny blog", apps[0].Description)

	rr = request(t, s.API(), "GET", "/v1/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var one apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &one))
	assert.Equal(t, "A tiny blog", one.Description)
}

func TestAPISetKeys(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	body := fmt.Sprintf(`{"ssh_keys":["%s"]}`, testPublicKey)
	rr = request(t, s.API(), "PUT", "/v1/apps/blog/keys", body, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "PUT", "/v1/apps/blog/keys", `{"ssh_keys":["junk"]}`, testToken)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "PUT", "/v1/apps/nope/keys", body, testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSocketSelf(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	s.usernameForUID = func(uid int) (string, error) {
		return "blog", nil
	}
	req := httptest.NewRequest("GET", "/v1/self", nil)
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.Name)
	assert.Equal(t, 10000, resp.Port)
	assert.Equal(t, "https://blog.apps.example.com", resp.URL)
}

func TestSocketSelfUnknownUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.usernameForUID = func(uid int) (string, error) {
		return "whoever", nil
	}
	req := httptest.NewRequest("GET", "/v1/self", nil)
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestAppResponseIncludesUsageAndOwner(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, token)
	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, s.apps.Store().UpdateAppUsage("blog", 42, true))

	// The owner sees usage and quota state on their own app
	rr = request(t, s.API(), "GET", "/v1/apps", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, 42, apps[0].DiskMB)
	assert.True(t, apps[0].OverQuota)

	// Admins additionally see who owns each app
	rr = request(t, s.API(), "GET", "/v1/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "owner@example.com", apps[0].OwnerEmail)
}

func TestSocketSelfLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	s.usernameForUID = func(uid int) (string, error) {
		return "blog", nil
	}
	// ensure: with nop ops this provisions and starts the workspace
	w := socketRequest(t, s, "POST", "/v1/self/ensure")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "workspace")
	// up: no hostit.yml in the (empty) app home -> 400
	w = socketRequest(t, s, "POST", "/v1/self/up")
	require.Equal(t, http.StatusBadRequest, w.Code)
	// down and restart succeed with nop runner
	require.Equal(t, http.StatusOK, socketRequest(t, s, "POST", "/v1/self/down").Code)
	require.Equal(t, http.StatusOK, socketRequest(t, s, "POST", "/v1/self/restart").Code)
	// status returns output (empty from nop runner, but 200)
	require.Equal(t, http.StatusOK, socketRequest(t, s, "GET", "/v1/self/status").Code)
	// logs: nothing yet -> error status
	require.NotEqual(t, http.StatusOK, socketRequest(t, s, "GET", "/v1/self/logs").Code)
}

func socketRequest(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	return w
}

func TestProxyRoutesToApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "yes")
		fmt.Fprintf(w, "hello from backend, host=%s", r.Host)
	}))
	t.Cleanup(backend.Close)
	registerAppWithBackend(t, s, "blog", backend.URL)
	rr := proxyRequest(t, s, "http://blog.apps.example.com/some/path")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "yes", rr.Header().Get("X-Backend"))
	assert.Contains(t, rr.Body.String(), "hello from backend")
	assert.Contains(t, rr.Body.String(), "host=blog.apps.example.com") // Host header preserved
}

func TestProxyHostWithPort(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(backend.Close)
	registerAppWithBackend(t, s, "blog", backend.URL)
	rr := proxyRequest(t, s, "http://blog.apps.example.com:8080/")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestProxyUnknownApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := proxyRequest(t, s, "http://nope.apps.example.com/")
	require.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
	body := rr.Body.String()
	assert.Contains(t, body, "nothing here")
	// A casual visitor must not learn whether the name is taken, nor how the
	// platform is wired up
	assert.NotContains(t, body, "hostit up")
	assert.NotContains(t, body, "ssh ")
	assert.NotContains(t, body, "127.0.0.1")
}

func TestProxyAppDownPage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	registerAppWithBackend(t, s, "blog", backend.URL)
	backend.Close()
	rr := proxyRequest(t, s, "http://blog.apps.example.com/")
	require.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
	body := rr.Body.String()
	// The visitor learns only that it is not running right now
	assert.Contains(t, body, "not running")
	// The owner gets actionable instructions, addressed to them
	assert.Contains(t, body, "ssh blog@apps.example.com")
	assert.Contains(t, body, "hostit up")
	assert.Contains(t, body, "hostit logs")
	// ... but no internals
	assert.NotContains(t, body, "127.0.0.1")
	assert.NotContains(t, body, "/srv/hostit")
	assert.NotContains(t, strings.ToLower(body), "port 1")
}

func TestProxyUnknownHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// The base domain now serves the web app, so only genuinely foreign or
	// multi-level hosts are nothing
	for _, u := range []string{"http://example.org/", "http://a.b.apps.example.com/"} {
		rr := proxyRequest(t, s, u)
		require.Equal(t, http.StatusNotFound, rr.Code, "url %s", u)
	}
}

func TestProxyAppDown(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	registerAppWithBackend(t, s, "blog", backend.URL)
	backend.Close() // Now nothing listens on the app port
	rr := proxyRequest(t, s, "http://blog.apps.example.com/")
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestProxyRoutesAPIHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := proxyRequest(t, s, "http://apps.example.com/v1/health")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"healthy":true`)
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	conf := config.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = testToken
	conf.AppsDir = t.TempDir()
	conf.DataDir = t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	manager := app.NewManager(conf, s, app.NewNopSystemOps(), app.NewNopRunner())
	return New(conf, manager, user.NewManager(conf, s))
}

// newActiveTestUser creates an approved user, as an admin would after approval
func newActiveTestUser(t *testing.T, s *Server, email string) *store.User {
	t.Helper()
	u, err := s.users.Login(email, "Test User")
	require.NoError(t, err)
	u.Status = store.StatusActive
	require.NoError(t, s.users.Update(u))
	return u
}

// registerAppWithBackend registers an app whose port points at the given test backend URL
func registerAppWithBackend(t *testing.T, s *Server, name, backendURL string) {
	t.Helper()
	u, err := url.Parse(backendURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: name, Port: port, Host: store.HostLocal}))
}

func request(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func proxyRequest(t *testing.T, s *Server, rawURL string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", rawURL, nil)
	rr := httptest.NewRecorder()
	s.proxyHandler().ServeHTTP(rr, req)
	return rr
}

func TestAppsListIsPersonalEvenForAdmins(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))
	adminToken, _, err := s.users.CreateToken(admin.ID, "t")
	require.NoError(t, err)
	other := newActiveTestUser(t, s, "someone@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "mine", Port: 10000, Host: store.HostLocal, OwnerID: admin.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "theirs", Port: 10001, Host: store.HostLocal, OwnerID: other.ID}))

	// The dashboard says "N of M apps used" next to this list, and that count is
	// the caller's own. Someone else's app in there is just confusing.
	names := func(token, query string) []string {
		rr := request(t, s.API(), "GET", "/v1/apps"+query, "", token)
		require.Equal(t, http.StatusOK, rr.Code)
		var apps []*apiAppResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
		out := make([]string, 0, len(apps))
		for _, a := range apps {
			out = append(out, a.Name)
		}
		return out
	}
	assert.Equal(t, []string{"mine"}, names(adminToken, ""), "an admin's own list is still their own")

	// The admin page asks for everything explicitly
	assert.Equal(t, []string{"mine", "theirs"}, names(adminToken, "?all=true"))

	// And only an admin may
	userToken, _, err := s.users.CreateToken(other.ID, "t")
	require.NoError(t, err)
	assert.Equal(t, []string{"theirs"}, names(userToken, ""))
	rr := request(t, s.API(), "GET", "/v1/apps?all=true", "", userToken)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}
