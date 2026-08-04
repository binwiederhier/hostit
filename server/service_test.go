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
	assert.Empty(t, resp.PrivateKey) // Key was provided, none generated
}

func TestAPICreateAppGeneratedKey(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Contains(t, resp.PrivateKey, "OPENSSH PRIVATE KEY")
	assert.Contains(t, resp.PublicKey, "ssh-ed25519")
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
}

func TestProxyUnknownHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, u := range []string{"http://example.org/", "http://apps.example.com/", "http://a.b.apps.example.com/"} {
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
	rr := proxyRequest(t, s, "http://hostit.apps.example.com/v1/health")
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
	manager := app.NewManager(conf, s, app.NewNopSystemOps(), app.NewNopUserRunner())
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
