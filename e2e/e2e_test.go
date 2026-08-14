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
	"bufio"
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
	e.get(fmt.Sprintf("/api/apps/%s/info", name), token, &info)
	guide, ok := info["guide"].(map[string]any)
	require.True(t, ok, "per-app info must inline the guide")
	assert.NotEmpty(t, guide["hostit_yml"])
	assert.NotEmpty(t, guide["endpoints"])
	assert.NotEmpty(t, guide["runtimes"])
	assert.Contains(t, strings.ToLower(fmt.Sprint(info["readme"])), "stub")

	// The skeleton placeholder serves immediately, before the agent touches anything
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	// Now act as the agent: upload a Go-free static site, deploy, verify
	e.put(fmt.Sprintf("/api/apps/%s/files/public/index.html", name), token, "<h1>e2e built this</h1>")
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: static\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "e2e built this")

	// Record what it is, as the prompt asks agents to do
	e.putJSON(fmt.Sprintf("/api/apps/%s/readme", name), token, map[string]string{
		"readme": "# " + name + "\n\nBuilt by the e2e suite.\n",
	})
	e.get(fmt.Sprintf("/api/apps/%s/info", name), token, &info)
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
	e.put(fmt.Sprintf("/api/apps/%s/files/server.py", name), token,
		"import http.server,os\n"+
			"class H(http.server.BaseHTTPRequestHandler):\n"+
			"    def do_GET(s):\n"+
			"        s.send_response(200); s.end_headers(); s.wfile.write(b'python is here')\n"+
			"http.server.HTTPServer(('0.0.0.0', int(os.environ['PORT'])), H).serve_forever()\n")
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: app\nrun: python3 server.py\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "python is here")

	// Logs must be readable when something goes wrong
	var logs map[string]any
	e.get(fmt.Sprintf("/api/apps/%s/logs?lines=20", name), token, &logs)
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
		"hostit.yml":        "mode: static\n",
	}
	for path, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	e.postRaw(fmt.Sprintf("/api/apps/%s/files", name), token, "application/x-tar", buf.Bytes())
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
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
	e.put(fmt.Sprintf("/api/apps/%s/files/serve.sh?mode=755", name), token,
		"#!/bin/sh\nexec python3 -m http.server \"$PORT\" --bind 0.0.0.0 --directory public\n")
	e.put(fmt.Sprintf("/api/apps/%s/files/public/index.html", name), token, "<h1>ran an uploaded program</h1>")
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token,
		"description: An app the e2e suite described\nmode: app\nrun: ./serve.sh\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "ran an uploaded program")

	// The description from hostit.yml is what the owner's page puts in the prompt
	var got map[string]any
	e.get("/api/apps/"+name, e.token, &got)
	assert.Equal(t, "An app the e2e suite described", got["description"])

	// A mode that is not octal is refused rather than silently ignored
	assert.Equal(t, http.StatusBadRequest, e.status("PUT", fmt.Sprintf("/api/apps/%s/files/x?mode=99", name), token))
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

	assert.Equal(t, http.StatusOK, e.status("GET", fmt.Sprintf("/api/apps/%s/info", mine), token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", fmt.Sprintf("/api/apps/%s/info", theirs), token))
	assert.Equal(t, http.StatusForbidden, e.status("POST", fmt.Sprintf("/api/apps/%s/restart", theirs), token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", "/api/apps", token))
	assert.Equal(t, http.StatusForbidden, e.status("GET", "/api/users", token))
	// Actions are POST-only, so a stray GET cannot restart anything
	assert.Equal(t, http.StatusMethodNotAllowed, e.status("GET", fmt.Sprintf("/api/apps/%s/restart", mine), token))
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
	e.waitForBody(url, "Nothing here yet")

	// A stopped app is deliberately indistinguishable from a hostname that belongs
	// to no app: both serve the same "nothing deployed" page, so a visitor cannot
	// tell a free name from a stopped app, and no internals (or the ssh login that
	// would confirm the app exists) leak.
	e.post(fmt.Sprintf("/api/apps/%s/stop", name), token, nil)
	body := e.waitForBody(url, "deployed here")
	assert.NotContains(t, body, "ssh "+name+"@", "a stopped app must not reveal its ssh login to visitors")
	assert.NotContains(t, body, "127.0.0.1", "no internals for visitors")

	// Starting it again brings the skeleton back
	e.post(fmt.Sprintf("/api/apps/%s/start", name), token, nil)
	e.waitForBody(url, "Nothing here yet")
}

// TestRootfsPersistsAcrossDeploy proves the container's filesystem is the app's
// persistent rootfs subvolume, not a throwaway image layer: a file planted
// outside the home (in /usr/local) must survive a config change that recreates
// the container. This is the apt-persistence promise -- installed packages no
// longer vanish on deploys and upgrades.
func TestRootfsPersistsAcrossDeploy(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-rootfs")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: static\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	// Plant a marker OUTSIDE the home directory: only the (rootfs) subvolume
	// holds it, so surviving proves the container's filesystem persists.
	_, code := e.runEventually(name, token, "touch /usr/local/persist-marker")
	require.Equal(t, 0, code, "planting the marker must succeed")

	// An env change alters the container config hash, which recreates the
	// container on deploy -- the exact path that used to lose installed packages.
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: static\nenv:\n  PERSIST: marker\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	out, code := e.runEventually(name, token, "ls /usr/local/persist-marker")
	assert.Equal(t, 0, code, "the marker must survive the container recreate; ls said: %s", out)
	assert.Contains(t, out, "persist-marker")
}

// TestRollbackRestoresInstalledSoftware proves snapshots capture the WHOLE app
// subvolume, not just the home: software installed outside the home (a marker
// in /usr/local, standing in for an apt package) comes back on rollback. This
// is the unified-layout promise -- rollback restores data AND installed
// software together.
func TestRollbackRestoresInstalledSoftware(t *testing.T) {
	e := newEnv(t)
	name := uniqueName("e2e-rollb")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: static\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	// "Install" something outside the home, then snapshot the whole app.
	_, code := e.runEventually(name, token, "touch /usr/local/rollback-marker")
	require.Equal(t, 0, code, "planting the marker must succeed")
	var snap map[string]any
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/snapshots", name), token, map[string]string{"label": "with marker"}, &snap, http.StatusOK)
	snapID := fmt.Sprint(snap["id"])
	require.NotEmpty(t, snapID)

	// Lose the "installed software", then roll back. The rollback swaps the
	// whole subvolume with the container powered down, so give it (and the app
	// coming back up) the same generous window as everything else here.
	_, code = e.runEventually(name, token, "rm /usr/local/rollback-marker")
	require.Equal(t, 0, code, "removing the marker must succeed")
	e.post(fmt.Sprintf("/api/apps/%s/snapshots/%s/restore", name, snapID), token, nil)

	out, code := e.runEventually(name, token, "ls /usr/local/rollback-marker")
	assert.Equal(t, 0, code, "the marker must be back after rollback; ls said: %s", out)
	assert.Contains(t, out, "rollback-marker")
}

// TestDiskHardCap proves the combined disk budget is a filesystem-enforced hard
// cap: a dd into the rootfs (outside the home, the layer that used to be
// uncapped) fails with EDQUOT/ENOSPC at the limit instead of filling the host.
func TestDiskHardCap(t *testing.T) {
	e := newEnv(t)
	// The cap of a new app comes from the instance default (there is no per-app
	// disk PATCH); set it small, restore afterwards.
	var settings map[string]any
	e.get("/api/settings", e.token, &settings)
	original := int(settings["default_disk_mb"].(float64))
	e.doJSON("PATCH", "/api/settings", e.token, map[string]any{"default_disk_mb": 200}, nil, http.StatusOK)
	t.Cleanup(func() {
		e.doJSON("PATCH", "/api/settings", e.token, map[string]any{"default_disk_mb": original}, nil, http.StatusOK)
	})

	name := uniqueName("e2e-cap")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	// 300 MB into /usr/bin against a 200 MB budget: the write itself must fail at
	// the cap -- no monitoring, no after-the-fact cutoff.
	out, code := e.runEventually(name, token, "dd if=/dev/zero of=/usr/bin/x bs=1M count=300")
	assert.NotEqual(t, 0, code, "dd past the budget must fail; output: %s", out)
	lower := strings.ToLower(out)
	assert.True(t, strings.Contains(lower, "quota") || strings.Contains(lower, "no space"),
		"dd must fail with EDQUOT/ENOSPC, got: %s", out)
}

// --- helpers ---

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}

func (e *env) createApp(name string) map[string]any {
	e.t.Helper()
	var app map[string]any
	e.doJSON("POST", "/api/apps", e.token, map[string]string{"name": name}, &app, http.StatusCreated)
	return app
}

func (e *env) deleteApp(name string) {
	e.t.Helper()
	req, err := http.NewRequest("DELETE", e.host+"/api/apps/"+name, nil)
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

// runEventually runs one shell command in the app's container via the /run API,
// retrying while the container is still coming up (a deploy recreates it, and
// exec needs it running). Returns the command's output and exit code.
func (e *env) runEventually(name, token, command string) (string, int) {
	e.t.Helper()
	body, err := json.Marshal(map[string]any{"command": command, "timeout_seconds": 180})
	require.NoError(e.t, err)
	deadline := time.Now().Add(appLive)
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("POST", e.host+fmt.Sprintf("/api/apps/%s/run", name), bytes.NewReader(body))
		require.NoError(e.t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 4 * time.Minute}
		resp, err := client.Do(req)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastStatus, lastBody = resp.StatusCode, string(raw)
			if resp.StatusCode == http.StatusOK {
				var res struct {
					Output   string `json:"output"`
					ExitCode int    `json:"exit_code"`
				}
				require.NoError(e.t, json.Unmarshal(raw, &res), "run %q: %s", command, lastBody)
				return res.Output, res.ExitCode
			}
		}
		time.Sleep(pollInterval)
	}
	e.t.Fatalf("run %q never got through; last status %d: %s", command, lastStatus, truncate(lastBody, 400))
	return "", -1
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

// --- Assistant mode selection: External Claude + API models ---

func TestAssistantCatalogAndPerAppModes(t *testing.T) {
	e := newEnv(t)
	d := e.assistantDefaults()
	require.NotEmpty(t, asSlice(d["models"]), "the API model catalog is offered")

	name := uniqueName("e2e-mode")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	var tr map[string]any
	e.get("/api/apps/"+name+"/assistant", e.token, &tr)
	ids := modeIDs(tr)
	require.NotEmpty(t, ids, "the app offers assistant modes")
	assert.NotEmpty(t, tr["mode"], "a default mode is resolved for the app")
	if contains(ids, "external-claude") {
		assert.Equal(t, true, d["external_configured"], "external offered only when the subscription is configured")
	}
}

func TestAssistantGlobalDefaultsFilterModes(t *testing.T) {
	e := newEnv(t)
	orig := e.assistantDefaults()
	t.Cleanup(func() {
		e.doJSON("PATCH", "/api/settings", e.token, map[string]any{
			"assistant": map[string]any{
				"external_allowed": orig["external_allowed"],
				"allowed_models":   orig["allowed_models"],
				"default_mode":     orig["default_mode"],
			},
		}, nil, http.StatusOK)
	})
	first := fmt.Sprint(asSlice(orig["models"])[0].(map[string]any)["id"])

	// Restrict the catalog to one model and disallow External Claude.
	e.doJSON("PATCH", "/api/settings", e.token, map[string]any{
		"assistant": map[string]any{
			"external_allowed": false,
			"allowed_models":   []string{first},
		},
	}, nil, http.StatusOK)

	name := uniqueName("e2e-filter")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })
	var tr map[string]any
	e.get("/api/apps/"+name+"/assistant", e.token, &tr)
	ids := modeIDs(tr)
	assert.NotContains(t, ids, "external-claude", "external is filtered out when disallowed")
	assert.Equal(t, []string{first}, ids, "only the allowed model is offered")
}

func TestAssistantPerUserOverride(t *testing.T) {
	e := newEnv(t)
	email := uniqueName("e2e-user") + "@example.com"
	var u map[string]any
	e.doJSON("POST", "/api/users", e.token, map[string]string{"email": email, "role": "user"}, &u, http.StatusCreated)
	uid := fmt.Sprint(u["id"])
	t.Cleanup(func() { e.status("DELETE", "/api/users/"+uid+"?apps=delete", e.token) })

	assert.Equal(t, false, u["assistant_has_override"], "a new user inherits the global default")

	e.doJSON("PATCH", "/api/users/"+uid, e.token, map[string]any{
		"assistant_external_allowed": false,
		"assistant_allowed_models":   []string{"claude-sonnet-5"},
	}, &u, http.StatusOK)
	assert.Equal(t, false, u["assistant_external_allowed"])
	assert.Equal(t, true, u["assistant_has_override"])

	e.doJSON("PATCH", "/api/users/"+uid, e.token, map[string]any{"assistant_clear_override": true}, &u, http.StatusOK)
	assert.Equal(t, false, u["assistant_has_override"], "clearing the override reverts to the default")
}

// TestAssistantEveryModelRunsATurn drives one real turn per configured model --
// what surfaced the Haiku "adaptive thinking not supported" bug -- and, when the
// subscription backend is configured, an External Claude turn followed by a model
// turn: the cross-backend switch the tool-id repair guards (it had 400'd on
// duplicate tool_result ids).
func TestAssistantEveryModelRunsATurn(t *testing.T) {
	e := newEnv(t)
	d := e.assistantDefaults()

	name := uniqueName("e2e-turn")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	const ask = "Reply with the single word: ok. Do not use any tools."
	for _, m := range asSlice(d["models"]) {
		id := fmt.Sprint(m.(map[string]any)["id"])
		types, turnErr := e.assistantTurn(name, id, ask)
		assert.Emptyf(t, turnErr, "model %s turn errored: %s", id, turnErr)
		assert.Containsf(t, types, "done", "model %s turn completed", id)
	}

	if d["external_configured"] == true {
		_, extErr := e.assistantTurn(name, "external-claude", "List the files, then say ok.")
		assert.Empty(t, extErr, "external turn should succeed")
		first := fmt.Sprint(asSlice(d["models"])[0].(map[string]any)["id"])
		_, apiErr := e.assistantTurn(name, first, ask)
		assert.Empty(t, apiErr, "switching to a model after External Claude must not fail on duplicate tool ids")
	}
}

func (e *env) assistantDefaults() map[string]any {
	// The assistant defaults are part of the global settings call, not a separate
	// endpoint: GET /api/settings returns them under "assistant".
	var s map[string]any
	e.get("/api/settings", e.token, &s)
	d, _ := s["assistant"].(map[string]any)
	return d
}

// assistantTurn starts a turn on the given mode and reads the event stream until
// the turn ends, returning the event types seen and the error it reported (empty
// on success).
func (e *env) assistantTurn(name, mode, message string) (types []string, turnErr string) {
	e.t.Helper()
	client := &http.Client{Timeout: 4 * time.Minute}
	req, err := http.NewRequest("GET", e.host+"/api/apps/"+name+"/assistant/stream", nil)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := client.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	time.Sleep(500 * time.Millisecond) // let the subscription register before starting

	e.doJSON("POST", "/api/apps/"+name+"/assistant", e.token,
		map[string]string{"message": message, "mode": mode}, nil, http.StatusAccepted)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		if line == sc.Text() { // keepalive comment, not a data frame
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
			continue
		}
		types = append(types, ev.Type)
		switch ev.Type {
		case "error":
			return types, ev.Error
		case "done":
			return types, ""
		}
	}
	return types, "stream ended without a done event"
}

func modeIDs(tr map[string]any) []string {
	var ids []string
	for _, m := range asSlice(tr["modes"]) {
		if mm, ok := m.(map[string]any); ok {
			ids = append(ids, fmt.Sprint(mm["id"]))
		}
	}
	return ids
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
