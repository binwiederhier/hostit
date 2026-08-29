//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Client ID Metadata Document an MCP authorization server fetches to
// identify hostit MUST be reachable publicly and WITHOUT auth: an authorization
// server has no hostit credentials, so a deploy that puts /.well-known/oauth-client
// behind the login wall silently breaks every MCP consent. This guards the whole
// path (proxy + control), which a handler unit test cannot see.
func TestMCPClientMetadataIsPubliclyReachable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	// No Authorization header, on purpose.
	resp, err := http.Get(e.host + "/.well-known/oauth-client")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the metadata document must be reachable without auth")
	assert.Contains(t, resp.Header.Get("Content-Type"), "json")

	var doc map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	assert.NotEmpty(t, doc["client_id"], "it identifies hostit by its client_id")
	assert.Equal(t, "hostit", doc["client_name"])
	assert.Equal(t, "none", doc["token_endpoint_auth_method"])
	uris, ok := doc["redirect_uris"].([]any)
	require.True(t, ok, "redirect_uris is present")
	assert.NotEmpty(t, uris, "and names at least one callback")
}

// The instance-wide prompt an admin sets shows up in the /info guide's notes,
// and clearing it removes it. Requires an admin token (settings are admin-only).
func TestInstancePromptReachesInfo(t *testing.T) {
	e := newEnv(t)
	marker := "E2E-INFO-PROMPT-" + uniqueName("m")
	e.doJSON("PATCH", "/api/settings", e.token, map[string]string{"info_prompt": marker}, nil, http.StatusOK)
	t.Cleanup(func() {
		e.doJSON("PATCH", "/api/settings", e.token, map[string]string{"info_prompt": ""}, nil, http.StatusOK)
	})

	var info map[string]any
	e.get("/api/info", e.token, &info)
	assert.Equal(t, marker, fmt.Sprint(info["additional_admin_prompt"]), "the instance prompt is its own /info field")

	// Cleared: the field is gone (omitempty) again. Poll briefly -- a real server
	// may take a beat to reflect the write on the read path.
	e.doJSON("PATCH", "/api/settings", e.token, map[string]string{"info_prompt": ""}, nil, http.StatusOK)
	assert.Eventually(t, func() bool {
		var got map[string]any
		e.get("/api/info", e.token, &got)
		return fmt.Sprint(got["additional_admin_prompt"]) == "<nil>"
	}, 10*time.Second, 500*time.Millisecond, "the prompt field is gone once cleared")
}

// Control's own journal is readable from the admin logs endpoint, and each node's
// too. Admin-only. journalctl must be present (it is on the systemd hosts hostit
// ships to); a host without it returns 500, which we surface rather than assert.
func TestAdminSystemLogs(t *testing.T) {
	e := newEnv(t)
	var control map[string]any
	e.get("/api/admin/logs/control", e.token, &control)
	assert.Equal(t, "control", control["source"])
	assert.NotEmpty(t, fmt.Sprint(control["text"]), "the control journal should not be empty")

	// One node too, by name from the cluster.
	var cluster map[string]any
	e.get("/api/cluster", e.token, &cluster)
	nodes, _ := cluster["nodes"].([]any)
	if len(nodes) == 0 {
		t.Skip("no nodes in the cluster")
	}
	node, _ := nodes[0].(map[string]any)
	name := fmt.Sprint(node["name"])
	var nodeLogs map[string]any
	e.get("/api/admin/logs/node/"+name, e.token, &nodeLogs)
	assert.Equal(t, "node", nodeLogs["source"])
	assert.Equal(t, name, nodeLogs["node"])
}

// The owner's per-app tab override round-trips and is normalized by the server
// (always a primary pane), and clearing it returns to no override.
func TestPerAppTabsOverride(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-tabs")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	var app map[string]any
	e.get("/api/apps/"+name, e.token, &app)
	assert.Equal(t, "", fmt.Sprint(app["tabs"]), "no override on a fresh app")

	// A set with a primary + extras round-trips; the server normalizes it.
	e.doJSON("PUT", "/api/apps/"+name+"/tabs", e.token, map[string]string{"tabs": "logs,files,terminal"}, &app, http.StatusOK)
	tabs := fmt.Sprint(app["tabs"])
	assert.Contains(t, tabs, "files", "a primary pane is present")
	assert.Contains(t, tabs, "terminal")
	assert.Contains(t, tabs, "logs")

	// A set with neither primary gains files (the assistant-or-files rule).
	e.doJSON("PUT", "/api/apps/"+name+"/tabs", e.token, map[string]string{"tabs": "terminal"}, &app, http.StatusOK)
	assert.Contains(t, fmt.Sprint(app["tabs"]), "files", "files is forced when nothing primary is chosen")

	// Empty clears the override.
	e.doJSON("PUT", "/api/apps/"+name+"/tabs", e.token, map[string]string{"tabs": ""}, &app, http.StatusOK)
	assert.Equal(t, "", fmt.Sprint(app["tabs"]), "an empty set clears the override")
}

// A PRIVATE app is screenshotted: its preview.png returns a real PNG, which only
// works because the shot browser presents an app-bound grant past the private
// gate. Before that, a private app's preview 404'd forever. Screenshot mode only.
func TestPrivateAppScreenshotBypass(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-priv")
	var app map[string]any
	e.doJSON("POST", "/api/apps", e.token, map[string]any{"name": name, "private": true}, &app, http.StatusCreated)
	t.Cleanup(func() { e.deleteApp(name) })
	if fmt.Sprint(app["preview_mode"]) != "screenshot" {
		t.Skip("server is not in screenshot preview mode")
	}

	e.put("/api/apps/"+name+"/files/public/index.html", e.token, "<h1>e2e private preview</h1>")
	e.put("/api/apps/"+name+"/files/hostit.yml", e.token, "mode: static\n")
	// Deploy waits for the app's port before returning (item 8), so the app is
	// serving once this call comes back. We do NOT fetch the app URL to confirm:
	// it is private, so an unauthenticated GET gets the sign-in page, not the
	// app. The screenshot below is the readiness proof that matters here -- it is
	// the only party that authenticates past the private gate.
	e.post("/api/apps/"+name+"/deploy", e.token, nil)

	// Force a shot and poll the PNG.
	e.doJSON("POST", "/api/apps/"+name+"/preview", e.token, nil, nil, http.StatusAccepted)
	deadline := time.Now().Add(2 * time.Minute)
	var status int
	var png []byte
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", e.host+"/api/apps/"+name+"/preview.png", nil)
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		status = resp.StatusCode
		if status == http.StatusOK {
			png, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			break
		}
		resp.Body.Close()
		time.Sleep(4 * time.Second)
	}
	require.Equal(t, http.StatusOK, status, "a private app must get a screenshot")
	require.GreaterOrEqual(t, len(png), 4)
	assert.Equal(t, []byte{0x89, 0x50, 0x4e, 0x47}, png[:4], "the shot is a PNG")
}

// An owner can invite a viewer by email before that person has an account: it
// shows up as pending in the viewer list and can be withdrawn. (The activation
// on first sign-in is covered by the store unit test.)
func TestPendingViewerInvite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-pend")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	email := "nobody-" + uniqueName("x") + "@example.com"
	e.doJSON("POST", "/api/apps/"+name+"/viewers", e.token, map[string]string{"email": email}, nil, http.StatusOK)

	var viewers []map[string]any
	e.get("/api/apps/"+name+"/viewers", e.token, &viewers)
	var found map[string]any
	for _, v := range viewers {
		if fmt.Sprint(v["email"]) == email {
			found = v
		}
	}
	require.NotNil(t, found, "the invited email is in the viewer list")
	assert.Equal(t, true, found["pending"], "and marked pending")
	assert.Equal(t, email, fmt.Sprint(found["id"]), "its id is the email, so remove round-trips")

	// Withdraw it (by email id).
	e.doJSON("DELETE", "/api/apps/"+name+"/viewers/"+email, e.token, nil, nil, http.StatusOK)
	e.get("/api/apps/"+name+"/viewers", e.token, &viewers)
	for _, v := range viewers {
		assert.NotEqual(t, email, fmt.Sprint(v["email"]), "the invite is gone")
	}
}
