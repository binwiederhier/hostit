package server

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentInfoIsSelfExplanatory(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/info", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	// An agent lands here first, so it must learn what this is and what to call
	assert.NotEmpty(t, resp.WhatIsThis)
	assert.NotEmpty(t, resp.Workflow)
	assert.NotEmpty(t, resp.Endpoints)
	assert.NotEmpty(t, resp.HostitYml)
	// An agent must learn what it can build with, and what we recommend
	assert.Contains(t, resp.Runtimes, "python3")
	assert.Contains(t, resp.Runtimes, "node")
	assert.Contains(t, resp.Runtimes, "go")
	assert.Contains(t, resp.Runtimes, "php")
	assert.Contains(t, resp.SuggestedStack, "Go binary")
	assert.Contains(t, resp.HostitYml, "static:")
	assert.Equal(t, "https://hostit.apps.example.com/api", resp.BaseURL)
	paths := make([]string, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		paths = append(paths, e.Method+" "+e.Path)
	}
	for _, want := range []string{
		"GET /api/{app}/info", "POST /api/{app}/deploy", "POST /api/{app}/restart",
		"PUT /api/{app}/files/{path}", "POST /api/{app}/files", "PUT /api/{app}/readme",
	} {
		assert.Contains(t, paths, want)
	}
}

func TestAgentInfoNeedsNoAppScope(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	rr := request(t, s.API(), "GET", "/api/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAgentAppInfoIncludesReadmeAndFiles(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	require.NoError(t, s.apps.WriteReadme("blog", "# blog\n\nThe finance dashboard.\n"))
	require.NoError(t, s.apps.WriteFile("blog", "index.html", []byte("<h1>hi</h1>")))
	// The no-op system ops in these tests does not scaffold, so write the config
	require.NoError(t, s.apps.WriteFile("blog", "hostit.yml", []byte("run: python3 -m http.server $PORT\n")))
	rr := request(t, s.API(), "GET", "/api/blog/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.Name)
	assert.Equal(t, "https://blog.apps.example.com", resp.URL)
	assert.Contains(t, resp.Readme, "finance dashboard")
	assert.Contains(t, resp.HostitYml, "run:")
	names := make([]string, 0, len(resp.Files))
	for _, f := range resp.Files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	assert.Contains(t, resp.SSH.Command, "ssh blog@")
}

func TestAgentTokenCannotTouchOtherApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	createTestApp(t, s, "other")
	for _, path := range []string{"/api/other/info", "/api/other/logs", "/api/other/files"} {
		rr := request(t, s.API(), "GET", path, "", token)
		require.Equal(t, http.StatusForbidden, rr.Code, "path %s", path)
	}
	rr := request(t, s.API(), "POST", "/api/other/restart", "", token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	// ... and it cannot reach the account-wide API either
	rr = request(t, s.API(), "POST", "/v1/apps", `{"name":"sneaky"}`, token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	rr = request(t, s.API(), "GET", "/v1/account/tokens", "", token)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAgentLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	for _, action := range []string{"stop", "restart", "start"} {
		rr := request(t, s.API(), "POST", "/api/blog/"+action, "", token)
		require.Equal(t, http.StatusOK, rr.Code, "action %s", action)
		assert.Contains(t, rr.Body.String(), "message")
	}
	// GET must not perform actions: a crawler or prefetch must not restart apps
	rr := request(t, s.API(), "GET", "/api/blog/restart", "", token)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestAgentUploadSingleFile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	rr := request(t, s.API(), "PUT", "/api/blog/files/static/app.js", "console.log(1)", token)
	require.Equal(t, http.StatusCreated, rr.Code)
	b, err := s.apps.ReadFile("blog", "static/app.js")
	require.NoError(t, err)
	assert.Equal(t, "console.log(1)", string(b))
	// Escapes never write outside the app: the router normalizes ".." away (307)
	// and app.WriteFile refuses what still gets through
	rr = request(t, s.API(), "PUT", "/api/blog/files/../../etc/passwd", "x", token)
	assert.NotEqual(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "PUT", "/api/blog/files/%2e%2e%2f%2e%2e%2fetc/passwd", "x", token)
	assert.NotEqual(t, http.StatusCreated, rr.Code)
	_, err = s.apps.ReadFile("blog", "etc/passwd")
	assert.Error(t, err)
}

func TestAgentUploadTar(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "print('hello')"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app.py", Mode: 0644, Size: int64(len(content))}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	req := httptest.NewRequest("POST", "/api/blog/files", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-tar")
	w := httptest.NewRecorder()
	s.API().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "app.py")
	b, err := s.apps.ReadFile("blog", "app.py")
	require.NoError(t, err)
	assert.Equal(t, content, string(b))
}

func TestAgentReadmeWrite(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	body := `{"readme":"# blog\n\nRebuilt as a dashboard on 2026-08-04.\n"}`
	rr := request(t, s.API(), "PUT", "/api/blog/readme", body, token)
	require.Equal(t, http.StatusOK, rr.Code)
	readme, err := s.apps.Readme("blog")
	require.NoError(t, err)
	assert.Contains(t, readme, "Rebuilt as a dashboard")
}

func TestAppGetsItsTokenOnCreation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "setup")
	require.NoError(t, err)

	// Creating an app hands back a token straight away: no extra click, no
	// second call, so the page can render the prompt immediately
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"blog"}`, userToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.AgentToken)

	// ... and it keeps showing the same token later, so it is never "lost"
	rr = request(t, s.API(), "GET", "/v1/apps/blog", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var fetched apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &fetched))
	assert.Equal(t, created.AgentToken, fetched.AgentToken)

	// The token really works, and only on this app
	rr = request(t, s.API(), "GET", "/api/blog/info", "", created.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)

	// The profile lists every token, each saying what it reaches, so the user
	// can tell an account token from an app token at a glance
	rr = request(t, s.API(), "GET", "/v1/account/tokens", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var listed []*apiTokenResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listed))
	scopes := make(map[string]bool)
	for _, tk := range listed {
		scopes[tk.AppName] = true
	}
	assert.True(t, scopes[""], "the account-wide token must be listed")
	assert.True(t, scopes["blog"], "the app-scoped token must be listed too")

	// Rotating replaces it and kills the old one
	rr = request(t, s.API(), "POST", "/v1/apps/blog/token", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var rotated apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rotated))
	assert.NotEqual(t, created.AgentToken, rotated.AgentToken)
	rr = request(t, s.API(), "GET", "/api/blog/info", "", created.AgentToken)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	rr = request(t, s.API(), "GET", "/api/blog/info", "", rotated.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAdminCreatedAppStillGetsAWorkingToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// The global admin token creates apps that belong to nobody; their agent
	// token must still work, so an admin can hand an app to someone
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"orphan"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.AgentToken)

	rr = request(t, s.API(), "GET", "/api/orphan/info", "", created.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)
	// ... and it is still confined to that app
	rr = request(t, s.API(), "GET", "/v1/apps", "", created.AgentToken)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAccountTokenReachesEveryAppOfTheUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	for _, name := range []string{"one", "two"} {
		rr := request(t, s.API(), "POST", "/v1/apps", fmt.Sprintf(`{"name":%q}`, name), userToken)
		require.Equal(t, http.StatusCreated, rr.Code)
	}
	// An account token drives every app the user owns, through the agent API too
	for _, name := range []string{"one", "two"} {
		rr := request(t, s.API(), "GET", "/api/"+name+"/info", "", userToken)
		assert.Equal(t, http.StatusOK, rr.Code, "account token must reach %s", name)
		rr = request(t, s.API(), "POST", "/api/"+name+"/restart", "", userToken)
		assert.Equal(t, http.StatusOK, rr.Code)
	}
	// ... but not somebody else's app
	other := newActiveTestUser(t, s, "other@example.com")
	otherToken, _, err := s.users.CreateToken(other.ID, "laptop")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"theirs"}`, otherToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "GET", "/api/theirs/info", "", userToken)
	assert.Equal(t, http.StatusNotFound, rr.Code, "one user's token must not reach another's app")
}

func TestAgentUnauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, path := range []string{"/api/info", "/api/blog/info"} {
		rr := request(t, s.API(), "GET", path, "", "")
		require.Equal(t, http.StatusUnauthorized, rr.Code, "path %s", path)
	}
}

// newAppToken creates an app owned by a fresh user plus a token scoped to it
func newAppToken(t *testing.T, s *Server, appName string) string {
	t.Helper()
	u := newActiveTestUser(t, s, appName+"-owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "setup")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/v1/apps", fmt.Sprintf(`{"name":%q}`, appName), userToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	token, _, err := s.users.CreateAppToken(u.ID, appName, "agent")
	require.NoError(t, err)
	return token
}

// createTestApp creates an app owned by someone else
func createTestApp(t *testing.T, s *Server, appName string) {
	t.Helper()
	rr := request(t, s.API(), "POST", "/v1/apps", fmt.Sprintf(`{"name":%q}`, appName), testToken)
	require.Equal(t, http.StatusCreated, rr.Code, strings.TrimSpace(rr.Body.String()))
}
