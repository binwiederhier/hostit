//go:build e2e

// Package e2e is the run-anytime regression suite for a REAL hostit server: it
// drives the public HTTP API exactly as users and agents do -- creating apps,
// uploading files, deploying, running commands, snapshotting and rolling back,
// forking, renaming, power-cycling -- and checks the results on the live app
// URLs. Run it with "make e2e" against a server you can afford to litter:
//
//	HOSTIT_HOST=https://hostit.apps.example.com HOSTIT_TOKEN=<admin token> make e2e
//
// Every test is independent, tolerates a slow (1-core) box via generous
// polling, and cleans up its e2e-* apps via t.Cleanup even on partial failure.
// Still: point it at a test/staging server, never at one hosting anything you
// care about. Expect the full suite to take on the order of 15-25 minutes; the
// journey tests added on top of the original suite contribute roughly 6 of
// those.
//
// The assistant tests are the only metered ones (they spend real model
// tokens); set HOSTIT_E2E_SKIP_ASSISTANT=1 to skip all of them. Of these,
// TestAssistantBuildsSomething is the only one that runs a full build turn: it
// is kept to exactly one turn on the cheapest model in the catalog.
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
	"sync"
	"sync/atomic"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestFullLifecycleJourney is a real user session as code, end to end on one
// app: build and deploy a small python app, "install" software and write data,
// snapshot, brick the app (lose the software, corrupt the data, even delete
// python itself), restore the snapshot, and verify that everything -- marker,
// data, interpreter, and the served page -- is back.
func TestFullLifecycleJourney(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-journey")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	e.requireSnapshots(app)
	token := fmt.Sprint(app["agent_token"])
	url := fmt.Sprint(app["url"])
	e.waitForBody(url, "Nothing here yet")

	// Build the app the way an agent would: upload files, write hostit.yml, deploy
	e.put(fmt.Sprintf("/api/apps/%s/files/server.py", name), token,
		"import http.server,os\n"+
			"class H(http.server.BaseHTTPRequestHandler):\n"+
			"    def do_GET(s):\n"+
			"        s.send_response(200); s.end_headers(); s.wfile.write(b'journey serves')\n"+
			"http.server.HTTPServer(('0.0.0.0', int(os.environ['PORT'])), H).serve_forever()\n")
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: app\nrun: python3 server.py\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(url, "journey serves")

	// "Install" something outside the home and write data inside it, then snapshot
	_, code := e.runEventually(name, token, "touch /usr/local/journey-install && echo v1 > /home/app/data.txt")
	require.Equal(t, 0, code, "planting the install marker and data must succeed")
	var snap map[string]any
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/snapshots", name), token, map[string]string{"label": "journey: known good"}, &snap, http.StatusOK)
	snapID := fmt.Sprint(snap["id"])
	require.NotEmpty(t, snapID)

	// Brick it: lose the installed software, corrupt the data, delete python
	// itself. (The already-running server keeps serving from its open inode.)
	_, code = e.runEventually(name, token, "rm -f /usr/local/journey-install /usr/bin/python3 && echo BRICKED > /home/app/data.txt")
	require.Equal(t, 0, code, "bricking the app must succeed")
	out, code := e.runEventually(name, token, "ls /usr/local/journey-install")
	require.NotEqual(t, 0, code, "the install marker must be gone before the restore; ls said: %s", out)

	// Restore: marker, data and interpreter come back together, and it serves
	e.post(fmt.Sprintf("/api/apps/%s/snapshots/%s/restore", name, snapID), token, nil)
	out, code = e.runEventually(name, token, "ls /usr/local/journey-install && cat /home/app/data.txt && python3 --version")
	assert.Equal(t, 0, code, "everything must be back after the restore; output: %s", out)
	assert.Contains(t, out, "v1")
	assert.NotContains(t, out, "BRICKED")
	e.waitForBody(url, "journey serves")
}

// TestForkCarriesEverything proves a fork (POST /api/apps/{name}/fork) copies
// the WHOLE app subvolume, not just the home: a marker in /usr/local and a file
// in the home both show up in the fork, and the fork serves on its own URL.
func TestForkCarriesEverything(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	source := uniqueName("e2e-fkr-src")
	app := e.createApp(source)
	t.Cleanup(func() {
		e.deleteApp(source)
	})
	e.requireSnapshots(app)
	token := fmt.Sprint(app["agent_token"])
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	// Plant one marker outside the home and one inside it
	_, code := e.runEventually(source, token, "touch /usr/local/fork-marker && echo carried > /home/app/fork-home.txt")
	require.Equal(t, 0, code, "planting the markers must succeed")

	// Fork, and clean the fork up too (registered only once it exists)
	forkName := uniqueName("e2e-fkr-new")
	var fork map[string]any
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/fork", source), e.token, map[string]string{"new_name": forkName}, &fork, http.StatusCreated)
	t.Cleanup(func() {
		e.deleteApp(forkName)
	})
	forkToken := fmt.Sprint(fork["agent_token"])
	require.NotEmpty(t, forkToken, "a fork comes with its own agent token")

	// Both markers made the trip, and the fork serves on its own URL
	out, code := e.runEventually(forkName, forkToken, "ls /usr/local/fork-marker && cat /home/app/fork-home.txt")
	assert.Equal(t, 0, code, "both markers must exist in the fork; output: %s", out)
	assert.Contains(t, out, "carried")
	e.waitForBody(fmt.Sprint(fork["url"]), "Nothing here yet")
}

// TestPowerCycleJourney powers an app's container off and on again: while off,
// visitors get the anonymous "nothing deployed" page, and the API refuses
// container-needing calls with a 409 that says why (powered off). Power on
// brings the skeleton back.
func TestPowerCycleJourney(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-power")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	token := fmt.Sprint(app["agent_token"])
	url := fmt.Sprint(app["url"])
	e.waitForBody(url, "Nothing here yet")

	// Power off: visitors see the same page a free hostname shows
	e.post(fmt.Sprintf("/api/apps/%s/poweroff", name), token, nil)
	e.waitForBody(url, "deployed here")

	// A /run against the powered-off container is refused, and the error says
	// power, not something cryptic. Poll: the poweroff may still be settling.
	status, body := e.waitForRunRefused(name, token)
	assert.Equal(t, http.StatusConflict, status, "a run against a powered-off app is a 409; body: %s", body)
	assert.Contains(t, strings.ToLower(body), "power", "the refusal must say the app is powered off; body: %s", body)

	// Starting the app process needs the container too, so it is refused the same way
	assert.Equal(t, http.StatusConflict, e.status("POST", fmt.Sprintf("/api/apps/%s/start", name), token))

	// Power on brings the skeleton back
	e.post(fmt.Sprintf("/api/apps/%s/poweron", name), token, nil)
	e.waitForBody(url, "Nothing here yet")
}

// TestSnapshotListAndDelete proves ids and labels round-trip through the
// snapshot API: two labelled snapshots list newest-first, and deleting one
// removes exactly that one. Automatic snapshots may interleave, so the test
// keys on its own ids rather than on absolute positions.
func TestSnapshotListAndDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-snaps")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	e.requireSnapshots(app)
	token := fmt.Sprint(app["agent_token"])

	// Two labelled snapshots; ids sort by second, so keep them a tick apart
	var first, second map[string]any
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/snapshots", name), token, map[string]string{"label": "e2e: first"}, &first, http.StatusOK)
	time.Sleep(1500 * time.Millisecond)
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/snapshots", name), token, map[string]string{"label": "e2e: second"}, &second, http.StatusOK)
	firstID, secondID := fmt.Sprint(first["id"]), fmt.Sprint(second["id"])
	require.NotEmpty(t, firstID)
	require.NotEmpty(t, secondID)
	require.NotEqual(t, firstID, secondID)

	// The list has both, newest first, with the labels intact
	var snaps []map[string]any
	e.get(fmt.Sprintf("/api/apps/%s/snapshots", name), token, &snaps)
	firstIdx, secondIdx := snapshotIndex(snaps, firstID), snapshotIndex(snaps, secondID)
	require.GreaterOrEqual(t, firstIdx, 0, "the first snapshot must be listed")
	require.GreaterOrEqual(t, secondIdx, 0, "the second snapshot must be listed")
	assert.Less(t, secondIdx, firstIdx, "snapshots list newest first")
	assert.Equal(t, "e2e: first", fmt.Sprint(snaps[firstIdx]["label"]))
	assert.Equal(t, "e2e: second", fmt.Sprint(snaps[secondIdx]["label"]))

	// Deleting one removes exactly that one
	e.doJSON("DELETE", fmt.Sprintf("/api/apps/%s/snapshots/%s", name, firstID), token, nil, nil, http.StatusOK)
	e.get(fmt.Sprintf("/api/apps/%s/snapshots", name), token, &snaps)
	assert.Equal(t, -1, snapshotIndex(snaps, firstID), "the deleted snapshot must be gone")
	assert.GreaterOrEqual(t, snapshotIndex(snaps, secondID), 0, "the other snapshot must survive")
}

// TestRenameKeepsAppAlive renames an app (POST /api/apps/{name}/rename): the
// app answers on its NEW subdomain with all its files intact (a rename moves
// nothing durable), and the old name is freed -- its API path 404s and its
// subdomain shows the anonymous "nothing deployed" page.
func TestRenameKeepsAppAlive(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	oldName, newName := uniqueName("e2e-ren-old"), uniqueName("e2e-ren-new")
	app := e.createApp(oldName)
	// Best-effort cleanup under both names: the rename may or may not have happened
	t.Cleanup(func() {
		e.deleteApp(newName)
		e.deleteApp(oldName)
	})
	token := fmt.Sprint(app["agent_token"])
	oldURL := fmt.Sprint(app["url"])
	e.waitForBody(oldURL, "Nothing here yet")

	// Plant a marker so we can prove the rename kept the app's files
	_, code := e.runEventually(oldName, token, "echo kept > /home/app/rename-marker.txt")
	require.Equal(t, 0, code, "planting the marker must succeed")

	// Rename; the response is the app under its new name, with its new URL
	var renamed map[string]any
	e.doJSON("POST", fmt.Sprintf("/api/apps/%s/rename", oldName), e.token, map[string]string{"new_name": newName}, &renamed, http.StatusOK)
	assert.Equal(t, newName, renamed["name"])
	newURL := fmt.Sprint(renamed["url"])
	require.NotEqual(t, oldURL, newURL)

	// Alive on the new subdomain, marker intact. The admin token runs the check:
	// it acts on any app, so the test does not depend on how the app-scoped token
	// migrates across a rename.
	e.waitForBody(newURL, "Nothing here yet")
	out, code := e.runEventually(newName, e.token, "cat /home/app/rename-marker.txt")
	assert.Equal(t, 0, code, "the marker must survive the rename; output: %s", out)
	assert.Contains(t, out, "kept")

	// The old name is freed: API 404, and the old subdomain shows the same page
	// a hostname that never had an app shows
	assert.Equal(t, http.StatusNotFound, e.status("GET", "/api/apps/"+oldName, e.token))
	e.waitForBody(oldURL, "deployed here")
}

// --- helpers ---

// nameCounter de-duplicates names minted in the same instant: the timestamp
// component cycles every 100us, and parallel tests mint names simultaneously.
var nameCounter atomic.Int64

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d%02d", prefix, time.Now().UnixNano()%100000, nameCounter.Add(1)%100)
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

// runOnce posts a single /run without retrying, returning the HTTP status and
// raw body -- for asserting on the refusal itself rather than waiting it out.
func (e *env) runOnce(name, token, command string) (int, string) {
	e.t.Helper()
	body, err := json.Marshal(map[string]any{"command": command, "timeout_seconds": 60})
	require.NoError(e.t, err)
	req, err := http.NewRequest("POST", e.host+fmt.Sprintf("/api/apps/%s/run", name), bytes.NewReader(body))
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// waitForRunRefused polls /run until the server refuses it with a 4xx (a
// poweroff may still be settling when the first call lands), returning that
// refusal's status and body.
func (e *env) waitForRunRefused(name, token string) (int, string) {
	e.t.Helper()
	deadline := time.Now().Add(appLive)
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		lastStatus, lastBody = e.runOnce(name, token, "true")
		if lastStatus >= 400 && lastStatus < 500 {
			return lastStatus, lastBody
		}
		time.Sleep(pollInterval)
	}
	e.t.Fatalf("run was never refused; last status %d: %s", lastStatus, truncate(lastBody, 400))
	return 0, ""
}

// requireSnapshots skips a test that needs the btrfs-backed features (snapshots,
// fork) on a host that does not have them; app is a fresh createApp response.
func (e *env) requireSnapshots(app map[string]any) {
	e.t.Helper()
	if enabled, ok := app["snapshots_enabled"].(bool); ok && !enabled {
		e.t.Skip("host does not support snapshots (no btrfs)")
	}
}

// snapshotIndex finds a snapshot id in a list response, -1 if absent
func snapshotIndex(snaps []map[string]any, id string) int {
	for i, s := range snaps {
		if fmt.Sprint(s["id"]) == id {
			return i
		}
	}
	return -1
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

// skipAssistantIfOptedOut honors HOSTIT_E2E_SKIP_ASSISTANT=1: the assistant
// tests are the suite's only metered ones (they spend real model tokens), so
// they can be opted out wholesale without touching anything else.
func skipAssistantIfOptedOut(t *testing.T) {
	t.Helper()
	if os.Getenv("HOSTIT_E2E_SKIP_ASSISTANT") == "1" {
		t.Skip("HOSTIT_E2E_SKIP_ASSISTANT=1: skipping the metered assistant tests")
	}
}

// assistantSettingsReset clears the server's assistant restrictions once per
// suite run. The mode-filtering test restricts them globally, and a run killed
// by -timeout (or a hand-run curl) skips its cleanup, so the NEXT run would
// otherwise start against a restricted catalog and fail tests that have
// nothing to do with restrictions. Resetting up front makes every run start
// from the same server state instead of inheriting the last one's.
var assistantSettingsReset sync.Once

// resetAssistantSettings returns the server to unrestricted assistant defaults:
// external allowed, no model allow-list. This IS the shipped default, so it is
// safe to assert as a baseline rather than echoing whatever was found.
func (e *env) resetAssistantSettings() {
	e.t.Helper()
	assistantSettingsReset.Do(func() {
		e.doJSON("PATCH", "/api/settings", e.token, map[string]any{
			"assistant": map[string]any{
				"external_allowed": true,
				"allowed_models":   []string{},
			},
		}, nil, http.StatusOK)
	})
}

func TestAssistantCatalogAndPerAppModes(t *testing.T) {
	skipAssistantIfOptedOut(t)
	e := newEnv(t)
	e.resetAssistantSettings()
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
	skipAssistantIfOptedOut(t)
	e := newEnv(t)
	e.resetAssistantSettings()
	orig := e.assistantDefaults()
	// Restore the unrestricted baseline rather than what was found: echoing the
	// observed state would perpetuate residue from an earlier killed run.
	t.Cleanup(func() {
		e.doJSON("PATCH", "/api/settings", e.token, map[string]any{
			"assistant": map[string]any{
				"external_allowed": true,
				"allowed_models":   []string{},
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
	skipAssistantIfOptedOut(t)
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
	skipAssistantIfOptedOut(t)
	e := newEnv(t)
	e.resetAssistantSettings()
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

// TestAssistantBuildsSomething drives exactly ONE real assistant turn that
// produces a served change: a deterministic "build this exact page and deploy"
// prompt, verified by polling the app's URL for the sentinel text. It picks the
// cheapest catalog model (this is the suite's one full metered build turn), and
// skips cleanly when the server has no assistant configured.
func TestAssistantBuildsSomething(t *testing.T) {
	skipAssistantIfOptedOut(t)
	e := newEnv(t)
	e.resetAssistantSettings()
	d := e.assistantDefaults()
	mode := cheapestMode(d)
	if mode == "" {
		t.Skip("no assistant configured on this server")
	}

	name := uniqueName("e2e-build")
	app := e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	// One turn, fully specified so the model has nothing to decide
	const ask = "Create the file public/index.html containing exactly the text ASSISTANT-BUILT-THIS " +
		"(nothing else, no markup). Write hostit.yml containing exactly the single line \"mode: static\". " +
		"Then deploy the app. Do nothing else."
	types, turnErr := e.assistantTurn(name, mode, ask)
	assert.Emptyf(t, turnErr, "the build turn errored: %s", turnErr)
	assert.Contains(t, types, "done", "the build turn must complete")

	// The proof is the live URL, not the transcript
	e.waitForBody(fmt.Sprint(app["url"]), "ASSISTANT-BUILT-THIS")
}

// cheapestMode picks the cheapest way to run a metered turn: the catalog has no
// prices, so a Haiku-family model wins by naming convention, then the first
// catalog model, then External Claude (subscription, so not metered per token).
// Empty means no assistant is configured at all.
func cheapestMode(d map[string]any) string {
	models := asSlice(d["models"])
	for _, m := range models {
		if mm, ok := m.(map[string]any); ok && strings.Contains(strings.ToLower(fmt.Sprint(mm["id"])), "haiku") {
			return fmt.Sprint(mm["id"])
		}
	}
	if len(models) > 0 {
		if mm, ok := models[0].(map[string]any); ok {
			return fmt.Sprint(mm["id"])
		}
	}
	if d["external_configured"] == true {
		return "external-claude"
	}
	return ""
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

// TestChurnDeleteRecreate proves the lifecycle stays fast and correct under
// rapid churn: delete answers immediately (rows first, host teardown in the
// background), and an immediate same-name recreate succeeds -- it waits out
// the dying unix user instead of failing with "already exists". The bounds are
// generous CI-style ceilings, not performance targets; what they catch is the
// return of the old behavior (multi-second deletes, 409s, or creates stalled
// ~12s behind a teardown's filesystem sync).
func TestChurnDeleteRecreate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-churn")
	app := e.createApp(name)
	t.Cleanup(func() {
		e.deleteApp(name)
	})
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")

	start := time.Now()
	e.deleteApp(name)
	deleteTook := time.Since(start)
	assert.Less(t, deleteTook, 5*time.Second, "delete must answer immediately, not wait for the teardown")

	// The rows are gone at once.
	req, err := http.NewRequest("GET", e.host+"/api/apps/"+name, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the app is gone from the API immediately")

	// Immediate same-name recreate: succeeds within the bounded teardown wait.
	start = time.Now()
	app = e.createApp(name)
	recreateTook := time.Since(start)
	assert.Less(t, recreateTook, 45*time.Second, "a same-name recreate waits out the teardown, bounded")
	e.waitForBody(fmt.Sprint(app["url"]), "Nothing here yet")
}

// TestAppPreviewModeContract checks the dashboard preview endpoints honor the
// server's configured app-preview mode. It is mode-agnostic: on a "screenshot"
// server a manual refresh is accepted and produces a PNG shortly after; on a
// "live"/"off" server the endpoints do not exist for callers. So it passes
// against both stage (screenshot) and a default (live) install.
func TestAppPreviewModeContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-preview")
	app := e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })
	token, _ := app["agent_token"].(string)

	// Serve real content so a screenshot has something to capture, and so we know
	// TLS + the app are actually up before we ask for a shot.
	e.put(fmt.Sprintf("/api/apps/%s/files/public/index.html", name), token, "<h1>preview e2e</h1>")
	e.put(fmt.Sprintf("/api/apps/%s/files/hostit.yml", name), token, "mode: static\n")
	e.post(fmt.Sprintf("/api/apps/%s/deploy", name), token, nil)
	e.waitForBody(fmt.Sprint(app["url"]), "preview e2e")

	// The app response advertises the server's preview mode
	var got map[string]any
	e.get("/api/apps/"+name, e.token, &got)
	mode := fmt.Sprint(got["preview_mode"])
	require.Contains(t, []string{"live", "screenshot", "off"}, mode, "preview_mode must be a known mode")

	previewPath := fmt.Sprintf("/api/apps/%s/preview.png", name)
	refreshPath := fmt.Sprintf("/api/apps/%s/preview", name)

	if mode != "screenshot" {
		// Not in screenshot mode: neither endpoint exists for callers
		assert.Equal(t, http.StatusNotFound, e.status("GET", previewPath, e.token), "preview.png only exists in screenshot mode")
		assert.Equal(t, http.StatusNotFound, e.status("POST", refreshPath, e.token), "manual refresh only exists in screenshot mode")
		return
	}

	// Screenshot mode: a manual refresh is accepted, then a PNG appears shortly
	assert.Equal(t, http.StatusAccepted, e.status("POST", refreshPath, e.token), "a manual refresh is queued")
	ct := e.waitForImage(previewPath, e.token)
	assert.Equal(t, "image/png", ct, "the stored shot is served as a PNG")
}

// waitForImage polls an authed endpoint until it returns a non-empty 200, then
// reports its Content-Type. Fails after the app-live deadline.
func (e *env) waitForImage(path, token string) string {
	e.t.Helper()
	deadline := time.Now().Add(appLive)
	lastStatus := 0
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", e.host+path, nil)
		require.NoError(e.t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			lastStatus = resp.StatusCode
			ct := resp.Header.Get("Content-Type")
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && len(b) > 0 {
				return ct
			}
		}
		time.Sleep(pollInterval)
	}
	e.t.Fatalf("%s never returned a 200 image (last status %d)", path, lastStatus)
	return ""
}
