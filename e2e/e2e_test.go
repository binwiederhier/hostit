//go:build e2e

// Package e2e drives a real, running hostit server over HTTPS: it creates apps,
// deploys them through the agent API, and checks that they actually serve. Run
// it with "make e2e" (see the Makefile for the environment it needs).
//
// These tests create and delete apps named e2e-*, so point them at a test
// server, never at one hosting anything you care about.
package e2e

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// appLive is how long an app gets to come up before we call it broken; the
	// first app on a fresh host also waits for the workspace image
	appLive = 5 * time.Minute
	// pollInterval is how often we re-check a URL while waiting
	pollInterval = 3 * time.Second
)

// env holds the server under test
type env struct {
	host  string // e.g. https://hostit.apps.example.com
	token string // admin token
	t     *testing.T
}

func newEnv(t *testing.T) *env {
	t.Helper()
	host, token := os.Getenv("HOSTIT_HOST"), os.Getenv("HOSTIT_TOKEN")
	if host == "" || token == "" {
		t.Skip("set HOSTIT_HOST and HOSTIT_TOKEN to run the e2e suite")
	}
	return &env{host: strings.TrimSuffix(host, "/"), token: token, t: t}
}

func TestAgentCanBuildAnAppFromNothing(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-agent")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})

	// The token comes with the app: this is what the user pastes into an agent
	token, _ := app["agent_token"].(string)
	require.NotEmpty(t, token, "an app must be created with its agent token")

	// Everything an agent needs must hang off this one URL
	var info map[string]any
	e.get(fmt.Sprintf("/api/%s/info", name), token, &info)
	guide, ok := info["guide"].(map[string]any)
	require.True(t, ok, "per-app info must inline the guide")
	assert.NotEmpty(t, guide["hostit_yml"])
	assert.NotEmpty(t, guide["endpoints"])
	assert.NotEmpty(t, guide["runtimes"])
	assert.Contains(t, strings.ToLower(fmt.Sprint(info["readme"])), "stub")

	// The stub serves immediately, before the agent touches anything
	e.waitForBody(fmt.Sprint(app["url"]), "stub")

	// Now act as the agent: upload a Go-free static site, deploy, verify
	e.put(fmt.Sprintf("/api/%s/files/index.html", name), token, "<h1>e2e built this</h1>")
	e.put(fmt.Sprintf("/api/%s/files/hostit.yml", name), token, "static: .\n")
	e.post(fmt.Sprintf("/api/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "e2e built this")

	// Record what it is, as the prompt asks agents to do
	e.putJSON(fmt.Sprintf("/api/%s/readme", name), token, map[string]string{
		"readme": "# " + name + "\n\nBuilt by the e2e suite.\n",
	})
	e.get(fmt.Sprintf("/api/%s/info", name), token, &info)
	assert.Contains(t, fmt.Sprint(info["readme"]), "Built by the e2e suite")
}

func TestRunModeWithARealRuntime(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-run")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])

	// A python app, proving the workspace runtimes are usable
	e.put(fmt.Sprintf("/api/%s/files/server.py", name), token,
		"import http.server,os\n"+
			"class H(http.server.BaseHTTPRequestHandler):\n"+
			"    def do_GET(s):\n"+
			"        s.send_response(200); s.end_headers(); s.wfile.write(b'python is here')\n"+
			"http.server.HTTPServer(('0.0.0.0', int(os.environ['PORT'])), H).serve_forever()\n")
	e.put(fmt.Sprintf("/api/%s/files/hostit.yml", name), token, "run: python3 server.py\n")
	e.post(fmt.Sprintf("/api/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "python is here")

	// Logs must be readable when something goes wrong
	var logs map[string]any
	e.get(fmt.Sprintf("/api/%s/logs?lines=20", name), token, &logs)
	assert.NotNil(t, logs["output"])
}

func TestTarUploadOfAWholeTree(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-tar")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := map[string]string{
		"public/index.html": "<h1>from a tarball</h1>",
		"public/app.css":    "body{}",
		"hostit.yml":        "static: public\n",
	}
	for path, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	e.postRaw(fmt.Sprintf("/api/%s/files", name), token, "application/x-tar", buf.Bytes())
	e.post(fmt.Sprintf("/api/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "from a tarball")
}

func TestExecutableUploadAndDescription(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-exec")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])

	// An uploaded program has to arrive runnable, or "run:" needs a chmod dance
	e.put(fmt.Sprintf("/api/%s/files/serve.sh?mode=755", name), token,
		"#!/bin/sh\nexec python3 -m http.server \"$PORT\" --bind 0.0.0.0\n")
	e.put(fmt.Sprintf("/api/%s/files/index.html", name), token, "<h1>ran an uploaded program</h1>")
	e.put(fmt.Sprintf("/api/%s/files/hostit.yml", name), token,
		"description: An app the e2e suite described\nrun: ./serve.sh\n")
	e.post(fmt.Sprintf("/api/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "ran an uploaded program")

	// The description from hostit.yml is what the owner's page puts in the prompt
	var got map[string]any
	e.get("/v1/apps/"+name, e.token, &got)
	assert.Equal(t, "An app the e2e suite described", got["description"])

	// A mode that is not octal is refused rather than silently ignored
	assert.Equal(t, http.StatusBadRequest, e.status("PUT", fmt.Sprintf("/api/%s/files/x?mode=99", name), token))
}

func TestAppTokenCannotLeaveItsApp(t *testing.T) {
	e := newEnv(t)
	mine, theirs := uniqueName("e2e-mine"), uniqueName("e2e-theirs")
	app := e.createApp(mine)
	e.createApp(theirs)
	t.Cleanup(func() {
		e.deleteApp(mine)
		e.deleteApp(theirs)
	})
	token := fmt.Sprint(app["agent_token"])

	assert.Equal(t, http.StatusOK, e.status("GET", fmt.Sprintf("/api/%s/info", mine), token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", fmt.Sprintf("/api/%s/info", theirs), token))
	assert.Equal(t, http.StatusForbidden, e.status("POST", fmt.Sprintf("/api/%s/restart", theirs), token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", "/v1/apps", token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", "/v1/users", token))
	// Actions are POST-only, so a stray GET cannot restart anything
	assert.Equal(t, http.StatusMethodNotAllowed, e.status("GET", fmt.Sprintf("/api/%s/restart", mine), token))
}

func TestUnknownAppAndStoppedApp(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-stop")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])
	url := fmt.Sprint(app["url"])
	e.waitForBody(url, "stub")

	// A stopped app shows the "not running" page, with owner instructions
	e.post(fmt.Sprintf("/api/%s/stop", name), token, nil)
	body := e.waitForBody(url, "not running")
	assert.Contains(t, body, "ssh "+name+"@", "the owner hint names the app's ssh login")
	assert.NotContains(t, body, "127.0.0.1", "no internals for visitors")

	// Starting it again brings the stub back
	e.post(fmt.Sprintf("/api/%s/start", name), token, nil)
	e.waitForBody(url, "stub")
}

// --- helpers ---

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}

func (e *env) createApp(name string) map[string]any {
	e.t.Helper()
	var app map[string]any
	e.doJSON("POST", "/v1/apps", e.token, map[string]string{"name": name}, &app, http.StatusCreated)
	return app
}

func (e *env) deleteApp(name string) {
	e.t.Helper()
	req, err := http.NewRequest("DELETE", e.host+"/v1/apps/"+name, nil)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (e *env) get(path, token string, out any) {
	e.t.Helper()
	e.doJSON("GET", path, token, nil, out, http.StatusOK)
}

func (e *env) post(path, token string, body any) {
	e.t.Helper()
	e.doJSON("POST", path, token, body, nil, http.StatusOK)
}

func (e *env) put(path, token, body string) {
	e.t.Helper()
	e.postRaw2("PUT", path, token, "text/plain", []byte(body), http.StatusCreated)
}

func (e *env) putJSON(path, token string, body any) {
	e.t.Helper()
	e.doJSON("PUT", path, token, body, nil, http.StatusOK)
}

func (e *env) postRaw(path, token, contentType string, body []byte) {
	e.t.Helper()
	e.postRaw2("POST", path, token, contentType, body, http.StatusCreated)
}

func (e *env) postRaw2(method, path, token, contentType string, body []byte, want int) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.host+path, bytes.NewReader(body))
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	require.Equal(e.t, want, resp.StatusCode, "%s %s: %s", method, path, string(got))
}

func (e *env) doJSON(method, path, token string, body, out any, want int) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.host+path, reader)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(e.t, want, resp.StatusCode, "%s %s: %s", method, path, string(raw))
	if out != nil {
		require.NoError(e.t, json.Unmarshal(raw, out), "%s %s: %s", method, path, string(raw))
	}
}

func (e *env) status(method, path, token string) int {
	e.t.Helper()
	req, err := http.NewRequest(method, e.host+path, nil)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// waitForBody polls a URL until its body contains want, and returns that body
func (e *env) waitForBody(url, want string) string {
	e.t.Helper()
	deadline := time.Now().Add(appLive)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			last = string(b)
			if strings.Contains(strings.ToLower(last), strings.ToLower(want)) {
				return last
			}
		}
		time.Sleep(pollInterval)
	}
	e.t.Fatalf("%s never contained %q; last body was:\n%s", url, want, truncate(last, 400))
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
