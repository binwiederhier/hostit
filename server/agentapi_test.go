package server

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
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
	assert.Contains(t, resp.Runtimes, "Node.js")
	assert.Contains(t, resp.Runtimes, "go")
	assert.Contains(t, resp.Runtimes, "PHP")
	assert.Contains(t, resp.SuggestedStack, "Go binary")
	assert.Contains(t, resp.HostitYml, "mode: static")
	assert.Equal(t, "https://apps.example.com/api", resp.BaseURL) // The base domain is the front door
	paths := make([]string, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		paths = append(paths, e.Method+" "+e.Path)
	}
	for _, want := range []string{
		"GET /api/apps/{app}/info", "POST /api/apps/{app}/deploy",
		"POST /api/apps/{app}/start|stop|restart", "POST /api/apps/{app}/poweron|poweroff|reboot",
		"PUT /api/apps/{app}/files/{path}", "POST /api/apps/{app}/files", "PUT /api/apps/{app}/readme",
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
	require.NoError(t, s.apps.WriteFile("blog", "index.html", []byte("<h1>hi</h1>"), 0))
	// The no-op system ops in these tests does not write a skeleton, so write the config
	require.NoError(t, s.apps.WriteFile("blog", "hostit.yml", []byte("mode: app\nrun: python3 -m http.server $PORT\n"), 0))
	rr := request(t, s.API(), "GET", "/api/apps/blog/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.Name)
	assert.Equal(t, "https://blog.apps.example.com", resp.URL)
	assert.Contains(t, resp.Readme, "finance dashboard")
	assert.Contains(t, resp.HostitYml, "run:")
	names := make([]string, 0, len(resp.Files.Files))
	for _, f := range resp.Files.Files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	assert.Contains(t, resp.SSH.Command, "ssh blog@")
}

func TestAgentGuideTellsAnExistingAppApartFromAStub(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")

	// A stub invites a rebuild
	var resp apiAgentAppResponse
	rr := request(t, s.API(), "GET", "/api/apps/blog/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Guide.Workflow)
	assert.Contains(t, resp.Guide.Workflow[0], "stub")
	assert.NotContains(t, strings.ToLower(resp.Guide.Workflow[0]), "already built")

	// Once the app describes itself it is finished work, and an agent that
	// starts over would destroy it
	require.NoError(t, s.apps.WriteFile("blog", "hostit.yml", []byte("description: The finance dashboard\nmode: static\n"), 0))
	rr = request(t, s.API(), "GET", "/api/apps/blog/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Contains(t, resp.Guide.Workflow[0], "already built")
	assert.Contains(t, resp.Guide.Workflow[0], "The finance dashboard")
	assert.Contains(t, resp.Guide.Workflow[0], "Do not rebuild")
	assert.NotContains(t, resp.Guide.Workflow[0], "stub")

	// The platform-wide guide belongs to no app and keeps the neutral wording
	rr = request(t, s.API(), "GET", "/api/info", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var guide apiAgentInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &guide))
	assert.Contains(t, guide.Workflow[0], "stub")
}

func TestAgentAssistantTranscriptGivesContextToAnExternalAgent(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")

	// Switch the assistant on with a store we can seed, as if the owner had already
	// chatted with the built-in assistant before switching to an external agent.
	sessions := assistant.NewMemoryStore()
	s.assistant = assistant.NewManager(assistant.NewClient("test-key"), &appOps{apps: s.apps}, sessions, "test-model")
	require.NoError(t, sessions.Save("blog", []assistant.Message{
		{Role: "user", Content: []assistant.ContentBlock{{Type: "text", Text: "add a dark mode toggle"}}},
		{Role: "assistant", Content: []assistant.ContentBlock{{Type: "text", Text: "Done, the toggle is in the header."}}},
	}))

	rr := request(t, s.API(), "GET", "/api/apps/blog/assistant/transcript", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentAssistantResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Enabled)
	// The app-scoped agent sees the whole prior conversation as readable text.
	assert.Contains(t, resp.Transcript, "add a dark mode toggle")
	assert.Contains(t, resp.Transcript, "Done, the toggle is in the header.")
}

func TestAgentAssistantTranscriptDisabledWhenUnconfigured(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // no Anthropic key -> assistant is nil
	token := newAppToken(t, s, "blog")
	rr := request(t, s.API(), "GET", "/api/apps/blog/assistant/transcript", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentAssistantResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.Enabled)
}

func TestAgentInfoAdvertisesTheAssistantTranscript(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/info", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	paths := make([]string, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		paths = append(paths, e.Method+" "+e.Path)
	}
	assert.Contains(t, paths, "GET /api/apps/{app}/assistant/transcript")
	// The workflow tells the agent to read it first, so it continues prior work
	// instead of starting cold.
	joined := strings.Join(resp.Workflow, "\n")
	assert.Contains(t, joined, "/assistant")
}

func TestAgentInfoInstructsPeriodicLabelledSnapshots(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/info", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAgentInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))

	// The snapshot endpoints are documented, so an agent knows how to take one.
	paths := make([]string, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		paths = append(paths, e.Method+" "+e.Path)
	}
	assert.Contains(t, paths, "POST /api/apps/{app}/snapshots")

	// The guide tells the agent to snapshot at regular intervals, and to attach a
	// short one-line description of why.
	guide := strings.ToLower(strings.Join(append(append([]string{}, resp.Workflow...), resp.Notes...), "\n"))
	assert.Contains(t, guide, "snapshot")
	assert.Contains(t, guide, "regular")
	assert.Contains(t, guide, "description")
}

func TestAgentTokenCannotTouchOtherApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	createTestApp(t, s, "other")
	for _, path := range []string{"/api/apps/other/info", "/api/apps/other/logs", "/api/apps/other/files"} {
		rr := request(t, s.API(), "GET", path, "", token)
		require.Equal(t, http.StatusForbidden, rr.Code, "path %s", path)
	}
	rr := request(t, s.API(), "POST", "/api/apps/other/restart", "", token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	// ... and it cannot reach the account-wide API either
	rr = request(t, s.API(), "POST", "/api/apps", `{"name":"sneaky"}`, token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	rr = request(t, s.API(), "GET", "/api/account/tokens", "", token)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAgentLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	for _, action := range []string{"stop", "restart", "start"} {
		rr := request(t, s.API(), "POST", "/api/apps/blog/"+action, "", token)
		require.Equal(t, http.StatusOK, rr.Code, "action %s", action)
		assert.Contains(t, rr.Body.String(), "message")
	}
	// GET must not perform actions: a crawler or prefetch must not restart apps
	rr := request(t, s.API(), "GET", "/api/apps/blog/restart", "", token)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestAgentUploadSingleFile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	rr := request(t, s.API(), "PUT", "/api/apps/blog/files/static/app.js", "console.log(1)", token)
	require.Equal(t, http.StatusCreated, rr.Code)
	b, err := s.apps.ReadFile("blog", "static/app.js")
	require.NoError(t, err)
	assert.Equal(t, "console.log(1)", string(b))
	// Escapes never write outside the app. The router normalizes a raw ".." away
	// with a redirect, so it never reaches the handler as written...
	rr = request(t, s.API(), "PUT", "/api/apps/blog/files/../../etc/passwd", "x", token)
	assert.GreaterOrEqual(t, rr.Code, 300)
	assert.Less(t, rr.Code, 400)
	// ...and an encoded ".." that does reach the handler is refused as a bad
	// request (a clean 4xx), not a crash and not a write.
	rr = request(t, s.API(), "PUT", "/api/apps/blog/files/%2e%2e%2f%2e%2e%2fetc/passwd", "x", token)
	assert.GreaterOrEqual(t, rr.Code, 400)
	assert.Less(t, rr.Code, 500)
	// The file lands nowhere: not inside the app, and not where the escape aimed
	// (two levels up from the app home, i.e. under AppsDir).
	_, err = s.apps.ReadFile("blog", "etc/passwd")
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(s.config.AppsDir, "etc", "passwd"))
	assert.True(t, os.IsNotExist(statErr), "traversal must not write above the app home")
}

func TestAgentUploadWithMode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	rr := request(t, s.API(), "PUT", "/api/apps/blog/files/server?mode=755", "#!/bin/sh\necho hi\n", token)
	require.Equal(t, http.StatusCreated, rr.Code)
	// A rejected mode says why rather than silently writing the default
	rr = request(t, s.API(), "PUT", "/api/apps/blog/files/server?mode=999", "x", token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "octal")
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
	req := httptest.NewRequest("POST", "/api/apps/blog/files", bytes.NewReader(buf.Bytes()))
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
	rr := request(t, s.API(), "PUT", "/api/apps/blog/readme", body, token)
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
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, userToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.AgentToken)

	// ... and it keeps showing the same token later, so it is never "lost"
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var fetched apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &fetched))
	assert.Equal(t, created.AgentToken, fetched.AgentToken)

	// The token really works, and only on this app
	rr = request(t, s.API(), "GET", "/api/apps/blog/info", "", created.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)

	// The profile lists account tokens only; an app's token lives on its page,
	// so it does not add a row here for every app the user owns
	rr = request(t, s.API(), "GET", "/api/account/tokens", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var listed []*apiTokenResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listed))
	require.NotEmpty(t, listed, "the account-wide token is listed")
	for _, tk := range listed {
		assert.Empty(t, tk.AppName, "app-scoped tokens stay on their app page")
	}

	// Rotating replaces it and kills the old one
	rr = request(t, s.API(), "POST", "/api/apps/blog/token", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var rotated apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rotated))
	assert.NotEqual(t, created.AgentToken, rotated.AgentToken)
	rr = request(t, s.API(), "GET", "/api/apps/blog/info", "", created.AgentToken)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/blog/info", "", rotated.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestAdminCreatedAppStillGetsAWorkingToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// The global admin token creates apps that belong to nobody; their agent
	// token must still work, so an admin can hand an app to someone
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"orphan"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.AgentToken)

	rr = request(t, s.API(), "GET", "/api/apps/orphan/info", "", created.AgentToken)
	assert.Equal(t, http.StatusOK, rr.Code)
	// ... and it is still confined to that app
	rr = request(t, s.API(), "GET", "/api/apps", "", created.AgentToken)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAccountTokenReachesEveryAppOfTheUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	for _, name := range []string{"one", "two"} {
		rr := request(t, s.API(), "POST", "/api/apps", fmt.Sprintf(`{"name":%q}`, name), userToken)
		require.Equal(t, http.StatusCreated, rr.Code)
	}
	// An account token drives every app the user owns, through the agent API too
	for _, name := range []string{"one", "two"} {
		rr := request(t, s.API(), "GET", "/api/apps/"+name+"/info", "", userToken)
		assert.Equal(t, http.StatusOK, rr.Code, "account token must reach %s", name)
		rr = request(t, s.API(), "POST", "/api/apps/"+name+"/restart", "", userToken)
		assert.Equal(t, http.StatusOK, rr.Code)
	}
	// ... but not somebody else's app
	other := newActiveTestUser(t, s, "other@example.com")
	otherToken, _, err := s.users.CreateToken(other.ID, "laptop")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"theirs"}`, otherToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/theirs/info", "", userToken)
	assert.Equal(t, http.StatusNotFound, rr.Code, "one user's token must not reach another's app")
}

func TestAgentUnauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, path := range []string{"/api/info", "/api/apps/blog/info"} {
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
	rr := request(t, s.API(), "POST", "/api/apps", fmt.Sprintf(`{"name":%q}`, appName), userToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	token, _, err := s.users.CreateAppToken(u.ID, appName, "agent")
	require.NoError(t, err)
	return token
}

// createTestApp creates an app owned by someone else
func createTestApp(t *testing.T, s *Server, appName string) {
	t.Helper()
	rr := request(t, s.API(), "POST", "/api/apps", fmt.Sprintf(`{"name":%q}`, appName), testToken)
	require.Equal(t, http.StatusCreated, rr.Code, strings.TrimSpace(rr.Body.String()))
}

func TestFileReadIsNeverRenderedAsAPage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	require.NoError(t, s.apps.WriteFile("blog", "evil.html", []byte("<script>alert(1)</script>"), 0))
	// This endpoint lives on the web app's own origin, and an admin may read any
	// user's files: a tenant's page must not execute there
	rr := request(t, s.API(), "GET", "/api/apps/blog/files/evil.html", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, rr.Header().Get("Content-Disposition"), "attachment")
	assert.Contains(t, rr.Body.String(), "<script>") // The content itself is intact
}

func TestLogLinesAreBounded(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	// "?lines=" reaches podman --tail and a tail of the log file; an absurd
	// value must not become an absurd allocation
	rr := request(t, s.API(), "GET", "/api/apps/blog/logs?lines=99999999", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, maxLogLines, logLines("99999999"))
	assert.Equal(t, agentLogLines, logLines("0"))
	assert.Equal(t, agentLogLines, logLines("nonsense"))
	assert.Equal(t, 50, logLines("50"))
}

func TestGuideExplainsTheLayoutAndTheBuildChoice(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/api/info", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var guide apiAgentInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &guide))

	// An agent must not have to guess where anything goes
	for _, dir := range []string{appctl.PublicDir, appctl.BinDir, appctl.LogDir, appctl.SrcDir} {
		assert.Contains(t, guide.Layout, dir+"/", "the layout must name %s/", dir)
	}
	assert.Contains(t, guide.HostitYml, "mode: static")

	// Not everyone can produce a linux/amd64 binary, and a binary-only app leaves
	// the next session nothing to edit: the guide should push towards keeping the
	// source here, while still saying an upload works
	assert.Contains(t, guide.SuggestedStack, "prepare:")
	assert.Contains(t, guide.SuggestedStack, appctl.SrcDir+"/")
	assert.Contains(t, guide.SuggestedStack, "prebuilt binary")
	assert.Contains(t, guide.Runtimes, "go")
}

// fileInfoJSON mirrors the app.FileInfo shape returned by the stat endpoint.
type fileInfoJSON struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Mime string `json:"mime"`
}

func TestAgentFileStat(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	require.NoError(t, s.apps.WriteFile("blog", "notes.txt", []byte("hello text"), 0))
	require.NoError(t, s.apps.WriteFile("blog", "public/logo.png", []byte("\x89PNG\r\n\x1a\n"), 0))
	require.NoError(t, s.apps.WriteFile("blog", "data", []byte("\x00\x01\x02bin\x00"), 0)) // no extension, binary

	// A text file: metadata only, a text/* MIME, and NOT the file's bytes -- the
	// editor uses this to avoid downloading a file just to learn what it is.
	rr := request(t, s.API(), "GET", "/api/apps/blog/files/notes.txt?stat=1", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
	assert.NotContains(t, rr.Body.String(), "hello text", "stat returns metadata, not the file body")
	var info fileInfoJSON
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
	assert.Equal(t, "file", info.Type)
	assert.Equal(t, int64(10), info.Size)
	assert.True(t, strings.HasPrefix(info.Mime, "text/"), "want text/* mime, got %q", info.Mime)

	// A known image extension resolves by extension.
	rr = request(t, s.API(), "GET", "/api/apps/blog/files/public/logo.png?stat=1", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
	assert.Equal(t, "image/png", info.Mime)

	// A no-extension binary is sniffed as a non-text type.
	rr = request(t, s.API(), "GET", "/api/apps/blog/files/data?stat=1", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
	assert.False(t, strings.HasPrefix(info.Mime, "text/"), "want binary mime, got %q", info.Mime)

	// A directory reports as a dir; without ?stat the endpoint still returns bytes.
	require.NoError(t, s.apps.WriteFile("blog", "sub/keep.txt", []byte("x"), 0))
	rr = request(t, s.API(), "GET", "/api/apps/blog/files/sub?stat=1", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
	assert.Equal(t, "dir", info.Type)
	rr = request(t, s.API(), "GET", "/api/apps/blog/files/notes.txt", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "hello text", rr.Body.String())

	// Scoped like everything else on the agent API.
	other := newAppToken(t, s, "other")
	rr = request(t, s.API(), "GET", "/api/apps/blog/files/notes.txt?stat=1", "", other)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAgentMkdir(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")

	// Create an empty folder (the file browser's "new folder").
	rr := request(t, s.API(), "POST", "/api/apps/blog/mkdir", `{"path":"assets/img"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	info, err := s.apps.StatFile("blog", "assets/img")
	require.NoError(t, err)
	assert.Equal(t, "dir", string(info.Type))

	// An empty path is a mistake; a repeat over an existing path is refused.
	rr = request(t, s.API(), "POST", "/api/apps/blog/mkdir", `{"path":""}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "POST", "/api/apps/blog/mkdir", `{"path":"assets/img"}`, token)
	assert.GreaterOrEqual(t, rr.Code, 400)

	// Traversal out of the home is refused and writes nothing above it.
	rr = request(t, s.API(), "POST", "/api/apps/blog/mkdir", `{"path":"../escape"}`, token)
	assert.GreaterOrEqual(t, rr.Code, 400)
	_, statErr := os.Stat(filepath.Join(s.config.AppsDir, "escape"))
	assert.True(t, os.IsNotExist(statErr))

	// Scoped to the app.
	other := newAppToken(t, s, "other")
	rr = request(t, s.API(), "POST", "/api/apps/blog/mkdir", `{"path":"x"}`, other)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAgentMove(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")
	require.NoError(t, s.apps.WriteFile("blog", "a.txt", []byte("hi"), 0))

	// Rename/move a file within the home (the file browser's drag + rename).
	rr := request(t, s.API(), "POST", "/api/apps/blog/move", `{"from":"a.txt","to":"public/b.txt"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.False(t, s.apps.FileExists("blog", "a.txt"))
	b, err := s.apps.ReadFile("blog", "public/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "hi", string(b))

	// from and to are both required; an existing destination is not clobbered.
	rr = request(t, s.API(), "POST", "/api/apps/blog/move", `{"from":"public/b.txt"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	require.NoError(t, s.apps.WriteFile("blog", "c.txt", []byte("x"), 0))
	rr = request(t, s.API(), "POST", "/api/apps/blog/move", `{"from":"c.txt","to":"public/b.txt"}`, token)
	assert.GreaterOrEqual(t, rr.Code, 400)
	assert.Equal(t, "hi", string(b)) // destination unchanged

	// Scoped to the app.
	other := newAppToken(t, s, "other")
	rr = request(t, s.API(), "POST", "/api/apps/blog/move", `{"from":"public/b.txt","to":"d.txt"}`, other)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAppResponseUsesVerifiedCustomDomain(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "setup")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, userToken)
	require.Equal(t, http.StatusCreated, rr.Code)

	get := func() apiAppResponse {
		rr := request(t, s.API(), "GET", "/api/apps/blog", "", userToken)
		require.Equal(t, http.StatusOK, rr.Code)
		var resp apiAppResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		return resp
	}

	// No custom domain, and a merely pending one, are not the app's primary URL.
	assert.Empty(t, get().CustomDomain)
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{Domain: "blog.example.com", AppName: "blog", Status: store.DomainPending}))
	assert.Empty(t, get().CustomDomain, "a pending domain is not used")

	// Once verified, it becomes the custom domain the web app links to.
	now := time.Now()
	require.NoError(t, s.apps.Store().SetDomainStatus("blog.example.com", store.DomainActive, "", &now))
	assert.Equal(t, "blog.example.com", get().CustomDomain)
}

func TestAppResponseReportsAssistantAvailability(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // no Anthropic key -> assistant is nil
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "setup")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, userToken)
	require.Equal(t, http.StatusCreated, rr.Code)

	// No key configured: the web app hides the Assistant tab and opens on the editor.
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.AssistantEnabled)

	// With the assistant configured, the field flips true and the tab appears.
	s.assistant = assistant.NewManager(assistant.NewClient("test-key"), &appOps{apps: s.apps}, assistant.NewMemoryStore(), "test-model")
	rr = request(t, s.API(), "GET", "/api/apps/blog", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.AssistantEnabled)
}

func TestAgentRunEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	token := newAppToken(t, s, "blog")

	rr := request(t, s.API(), "POST", "/api/apps/blog/run", `{"command":"go build ./..."}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
	var res apiRunResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &res))
	assert.Equal(t, 0, res.ExitCode)

	// An empty command is a mistake; a GET is not a way to run anything
	rr = request(t, s.API(), "POST", "/api/apps/blog/run", `{"command":""}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/blog/run", "", token)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)

	// And it is scoped like everything else on the agent API
	other := newAppToken(t, s, "other")
	rr = request(t, s.API(), "POST", "/api/apps/blog/run", `{"command":"whoami"}`, other)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}
