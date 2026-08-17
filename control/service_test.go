package control

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
	"heckel.io/hostit/control/apptest"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	testToken = "secr3t"
	// testPublicKey lives in manager_test.go; testPublicKey2 is a second key
	// for handler tests that need two distinct ones.
	testPublicKey2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEARLXvgHdnZfqNlXFEr2sf0hTWutnDbJjDTfWjKO08k test2@host"
)

func TestAPIHealth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/health", "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"healthy":true`)
}

func TestAPIUnauthorized(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, token := range []string{"", "wrong"} {
		rr := request(t, s.API(), "GET", "/api/apps", "", token)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	}
}

func TestAPICreateApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body := fmt.Sprintf(`{"name":"blog","ssh_keys":["%s"]}`, testPublicKey)
	rr := request(t, s.API(), "POST", "/api/apps", body, testToken)
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
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
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
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"NOPE!"}`, testToken)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAPICreateAppDuplicate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestAPIListGetDeleteApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/apps/blog", "", testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAPIInviteUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/users", `{"email":"NewHire@allowed.example","role":"user"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "newhire@allowed.example", created.Email)
	assert.Equal(t, store.StatusActive, created.Status, "an invited user does not wait for approval")
	assert.Equal(t, store.RoleUser, created.Role)

	// They show up in the user list right away, before ever signing in
	rr = request(t, s.API(), "GET", "/api/users", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var users []*apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	assert.Equal(t, "newhire@allowed.example", users[0].Email)

	// Duplicates and junk are refused with a reason, not a 500
	rr = request(t, s.API(), "POST", "/api/users", `{"email":"newhire@allowed.example"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "POST", "/api/users", `{"email":"not-an-email"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAPIAllowedDomains(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/domains", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "[]", strings.TrimSpace(rr.Body.String()))

	// The admin types what they mean; hostit stores the bare domain
	rr = request(t, s.API(), "POST", "/api/domains", `{"domain":"*@allowed.example"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.Equal(t, "allowed.example", created.Domain)

	rr = request(t, s.API(), "GET", "/api/domains", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var domains []*apiDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &domains))
	require.Len(t, domains, 1)
	assert.Equal(t, "allowed.example", domains[0].Domain)

	// And it works: the next sign-in from that domain needs no approval
	u, err := s.users.Login("newhire@allowed.example", "New Hire")
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, u.Status)

	rr = request(t, s.API(), "POST", "/api/domains", `{"domain":"nope"}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/domains/allowed.example", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/domains/allowed.example", "", testToken)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestAPIAppDescription(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, s.apps.testMachine().WriteFile("blog", "hostit.yml", []byte("description: A tiny blog\nmode: static\n"), 0))

	// Both the list and the single app carry it, so the page can build the
	// prompt from whatever the app says it is
	rr = request(t, s.API(), "GET", "/api/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "A tiny blog", apps[0].Description)

	rr = request(t, s.API(), "GET", "/api/apps/blog", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var one apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &one))
	assert.Equal(t, "A tiny blog", one.Description)
}

func TestAPISetKeys(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	body := fmt.Sprintf(`{"ssh_keys":["%s"]}`, testPublicKey)
	rr = request(t, s.API(), "PUT", "/api/apps/blog/keys", body, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "PUT", "/api/apps/blog/keys", `{"ssh_keys":["junk"]}`, testToken)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "PUT", "/api/apps/nope/keys", body, testToken)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSocketSelf(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
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

func TestSocketServesOperatorAPIForRoot(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// Root over the socket reaches the same /api the TCP listeners serve, with
	// no token: the peer UID is the credential
	req := httptest.NewRequest("GET", "/api/apps", nil)
	req = req.WithContext(withPeerUID(req.Context(), 0))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	// A non-root peer gets what a tokenless TCP caller gets: nothing
	req = httptest.NewRequest("GET", "/api/apps", nil)
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w = httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	// Only /api is mounted; the web app and OAuth login stay off the socket
	for _, path := range []string{"/", "/auth/google"} {
		req = httptest.NewRequest("GET", path, nil)
		req = req.WithContext(withPeerUID(req.Context(), 0))
		w = httptest.NewRecorder()
		s.socketHandler().ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "path %s must not exist on the socket", path)
	}
}

func TestAppResponseIncludesUsageAndOwner(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, token)
	require.Equal(t, http.StatusCreated, rr.Code)
	require.NoError(t, s.apps.Store().UpdateAppUsage("blog", 42))

	// The owner sees measured disk usage on their own app
	rr = request(t, s.API(), "GET", "/api/apps", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var apps []*apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, 42, apps[0].DiskMB)

	// Admins additionally see who owns each app
	rr = request(t, s.API(), "GET", "/api/apps", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &apps))
	require.Len(t, apps, 1)
	assert.Equal(t, "owner@example.com", apps[0].OwnerEmail)
}

func TestSocketSelfLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	s.usernameForUID = func(uid int) (string, error) {
		return "blog", nil
	}
	// poweron/ensure: with nop ops this provisions and starts the workspace
	w := socketRequest(t, s, "POST", "/v1/self/poweron")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "workspace")
	// deploy: no hostit.yml in the (empty) app home -> 400
	w = socketRequest(t, s, "POST", "/v1/self/deploy")
	require.Equal(t, http.StatusBadRequest, w.Code)
	// the app-process and container verbs succeed with the nop runner
	for _, verb := range []string{"start", "stop", "restart", "poweroff", "reboot"} {
		require.Equal(t, http.StatusOK, socketRequest(t, s, "POST", "/v1/self/"+verb).Code, verb)
	}
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
	assert.Contains(t, body, "Error 404")
	assert.Contains(t, body, "No app answers at this address")
	// A casual visitor must not learn whether the name is taken, nor how the
	// platform is wired up
	assert.NotContains(t, body, "hostit up")
	assert.NotContains(t, body, "ssh ")
	assert.NotContains(t, body, "127.0.0.1")
}

func TestProxyAppDownIsIndistinguishableFromUnknown(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	registerAppWithBackend(t, s, "blog", backend.URL)
	backend.Close() // Now nothing listens on the app port

	down := proxyRequest(t, s, "http://blog.apps.example.com/")
	unknown := proxyRequest(t, s, "http://nope.apps.example.com/")
	// A registered-but-stopped app must look exactly like a free name, or a
	// visitor could enumerate which app names are taken. Same status, same body.
	assert.Equal(t, unknown.Code, down.Code)
	assert.Equal(t, unknown.Body.String(), down.Body.String())
	body := down.Body.String()
	// And the page names neither the app nor how the platform is wired up
	assert.NotContains(t, body, "blog")
	assert.NotContains(t, body, "ssh ")
	assert.NotContains(t, body, "hostit up")
	assert.NotContains(t, body, "127.0.0.1")
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

func TestProxyRoutesAPIHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := proxyRequest(t, s, "http://apps.example.com/api/health")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"healthy":true`)
}

func TestBreakglassLogin(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AdminEmails = []string{"phil@example.com"}

	// Off by default: invisible even with the admin token.
	rr := request(t, s.API(), "POST", "/auth/breakglass?email=phil@example.com", "", testToken)
	assert.Equal(t, http.StatusNotFound, rr.Code)

	s.config.Breakglass = true
	// No/wrong token is refused, so enabling it does not open a hole.
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/auth/breakglass?email=phil@example.com", "", "").Code)
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/auth/breakglass?email=phil@example.com", "", "wrong").Code)
	// An unknown, non-admin email is refused: breakglass will not conjure users.
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/auth/breakglass?email=stranger@example.com", "", testToken).Code)

	// An existing non-admin user can be signed in (viewing the app as them for e2e).
	newActiveTestUser(t, s, "member@example.com")
	assert.Equal(t, http.StatusOK, request(t, s.API(), "POST", "/auth/breakglass?email=member@example.com", "", testToken).Code)

	// Admin token + admin email mints a working session cookie (created on the spot).
	rr = request(t, s.API(), "POST", "/auth/breakglass?email=phil@example.com", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.NotEmpty(t, cookies, "a session cookie must be set")

	// The cookie authenticates a follow-up request as that admin.
	req := httptest.NewRequest("GET", "/api/account", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.API().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "phil@example.com")
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	conf := controlconf.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = testToken
	conf.AppsDir = t.TempDir()
	conf.DataDir = t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	manager := newWiredManager(t, conf, s, apptest.NewNopServices())
	// LIFO: registered after the store-close cleanup, so background goroutines
	// (post-create starts, delete teardowns) finish before the db closes.
	t.Cleanup(manager.WaitBackground)
	return New(conf, manager, user.NewManager(conf, s))
}

// newActiveTestUser creates an approved user, as an admin would after approval
func TestTLSIsOnlyIssuedForTheWebAppAndRegisteredApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	// Allowed: the web app itself and a real app's subdomain
	assert.NoError(t, s.allowTLSHost("apps.example.com"))
	assert.NoError(t, s.allowTLSHost("blog.apps.example.com"))
	// Refused: unknown names, nested labels and foreign domains, so a stranger
	// pointing DNS at the box cannot make us burn the ACME rate limit
	assert.Error(t, s.allowTLSHost("nosuchapp.apps.example.com"))
	assert.Error(t, s.allowTLSHost("a.b.apps.example.com"))
	assert.Error(t, s.allowTLSHost("evil.example.org"))
}

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
		rr := request(t, s.API(), "GET", "/api/apps"+query, "", token)
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
	rr := request(t, s.API(), "GET", "/api/apps?all=true", "", userToken)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestUnknownAPIPathIsNotAWebPage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// The SPA catch-all answers unknown paths with index.html, which is right for
	// a browser and useless to an agent: under /api that has to be a JSON 404,
	// including for the paths this API used to have.
	for _, path := range []string{"/api/nope", "/api/blog/info", "/api/apps/blog/nope", "/v1/apps"} {
		rr := request(t, s.API(), "GET", path, "", testToken)
		assert.Equal(t, http.StatusNotFound, rr.Code, "path %s", path)
		assert.NotContains(t, rr.Body.String(), "<!doctype html>", "path %s must not answer with the web app", path)
	}
}

// SetNode must repoint the ASSISTANT's tools too, not just the handlers: the
// external-backend tool loop acts through appOps.node, and leaving it on the
// local Manager makes assistant deploys build container args from control's
// own paths for an app hosted on another node (turn "succeeds", app never
// changes -- seen live on the two-node stage).
func TestSetNodeRepointsTheAssistantOps(t *testing.T) {
	t.Parallel()
	conf := controlconf.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = testToken
	conf.AppsDir = t.TempDir()
	conf.DataDir = t.TempDir()
	conf.AnthropicAPIKey = "sk-test" // makes New wire the assistant + its appOps
	st, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	apps := NewManager(conf, st)
	t.Cleanup(apps.WaitBackground)
	s := New(conf, apps, user.NewManager(conf, st))
	require.NotNil(t, s.assistantOps, "an assistant-configured server tracks its appOps")

	routed := &snapAgent{NodeAgent: apps.NodeAgent()}
	s.SetNode(routed)

	assert.Same(t, any(routed), any(s.node), "the handlers' agent is swapped")
	assert.Same(t, any(routed), any(s.assistantOps.node), "the assistant's tools must follow the same agent")
}

// keyWriter records the key sets control asks a node to write, standing in for
// the node's authorized_keys write.
type keyWriter struct {
	NodeAgent
	appKeys     []string
	profileKeys []string
	calls       int
}

func (w *keyWriter) SetKeys(name string, appKeys, profileKeys []string) error {
	w.appKeys, w.profileKeys, w.calls = appKeys, profileKeys, w.calls+1
	return nil
}

// Re-syncing an app's keys (a collaborator added or removed, a profile key
// changed) must keep the app's OWN keys. Control owns them -- they are
// registry state -- so it resolves them and hands the node the full set. The
// node used to be asked for a key set it did not have: app_key is not in the
// pushed mirror, so on a split deployment it read an empty list and rewrote
// authorized_keys with profile keys only, silently locking the owner out
// (reproduced on stage: adding a collaborator emptied the managed block).
func TestResyncAppKeysKeepsTheAppsOwnKeys(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", fmt.Sprintf(`{"name":"blog","ssh_keys":["%s"]}`, testPublicKey), testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	writer := &keyWriter{NodeAgent: s.apps.NodeAgent()}
	s.SetNode(writer)

	app, err := s.apps.App("blog")
	require.NoError(t, err)
	require.NoError(t, s.resyncAppKeys(app))

	require.Equal(t, 1, writer.calls, "the node is told the whole key set, not asked for it")
	assert.Equal(t, []string{testPublicKey}, writer.appKeys, "the app's own keys must survive a resync")
}

// Setting an app's keys must persist them in the REGISTRY, not only on the
// node's disk: control is where they live now, and the next resync (a
// collaborator change) reads them back from there.
func TestSetKeysPersistsThemInTheRegistry(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)

	rr = request(t, s.API(), "PUT", "/api/apps/blog/keys", fmt.Sprintf(`{"ssh_keys":["%s"]}`, testPublicKey2), testToken)
	require.Equal(t, http.StatusOK, rr.Code)

	stored, err := s.apps.Store().AppKeys("blog")
	require.NoError(t, err)
	assert.Equal(t, []string{testPublicKey2}, stored, "the registry owns the app's keys")
}

// The assistant sandbox resolves an app's uid from the registry, so a turn
// works for an app on ANY node. It used to read this host's passwd file, so an
// app on a remote node failed with "cannot resolve app user ... is the app
// deployed on this host?" and silently fell back to the metered API model
// (seen on stage the day the second node went in).
func TestAssistantSandboxIdentityComesFromTheRegistry(t *testing.T) {
	t.Parallel()
	conf, st := newProxyTestDeps(t)
	conf.ClaudeCodeOAuthToken = "sk-test"
	apps := NewManager(conf, st)
	t.Cleanup(apps.WaitBackground)
	s := New(conf, apps, user.NewManager(conf, st))
	require.NotNil(t, s.claudeSandbox, "the subscription backend is wired when its token is configured")

	// An app that lives on another node: no unix account on this host at all.
	require.NoError(t, st.AddApp(&store.App{ID: "id9", Name: "elsewhere", Port: 10500, Host: "worker-2", UID: 1327104}))

	uid, gid, appID, err := s.claudeSandbox.Identity("elsewhere")
	require.NoError(t, err)
	assert.Equal(t, 1327104, uid)
	assert.Equal(t, 1327104, gid)
	assert.Equal(t, "id9", appID)
}
