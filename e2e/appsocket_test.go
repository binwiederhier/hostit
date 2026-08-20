//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two regressions found on a multi-node stage, kept from coming back.
//
// Both had the same root: something that must happen on the app's node was
// served by control's host instead, so it worked exactly as long as every app
// sat next to control -- which prod (single host) always did, and stage-2 never
// did. These tests run against whatever node placement picks, so on a
// multi-node instance they cover the remote path and on a single host they
// still assert the behavior.
//
//  1. The app socket: /run/hostit/hostit.sock was control's listener, so an
//     app on a node-only host had none -- "hostit logs" inside the container
//     answered "cannot reach hostit daemon".
//  2. The browser terminal: control executed the node-supplied shell command
//     on its own host, so a remote-node terminal died at "runuser: user <app>
//     does not exist" before the shell ever started.

// TestInContainerCLIWorksWhereverPlaced proves the app socket from inside the
// container: status and logs answer, and a deploy issued from INSIDE the
// container reaches the live URL. The exact failure string of the old bug is
// asserted absent, so a regression fails with a recognizable message rather
// than a generic mismatch.
func TestInContainerCLIWorksWhereverPlaced(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-socket")
	app := e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })
	token, _ := app["agent_token"].(string)

	// The in-container CLI reaches the daemon through the app socket. Before
	// the fix this was the first thing to die on a remote node.
	out, code := e.runEventually(name, token, "hostit status 2>&1")
	assert.NotContains(t, out, "cannot reach hostit daemon", "the app socket must exist on the app's own node")
	assert.Zero(t, code, "hostit status: %s", truncate(out, 300))
	assert.Contains(t, out, "hostit-app@", "status shows the app's own unit")

	out, code = e.runEventually(name, token, "hostit logs 2>&1 | head -3")
	assert.NotContains(t, out, "cannot reach hostit daemon")
	assert.Zero(t, code, "hostit logs: %s", truncate(out, 300))

	// A deploy issued from INSIDE the container: write the page, deploy, and
	// see it on the public URL. This is the whole socket -> relay -> control ->
	// node round trip with a caller waiting on the answer.
	marker := "SOCKET-DEPLOYED-" + name
	out, code = e.runEventually(name, token,
		"echo '"+marker+"' > public/index.html && hostit deploy 2>&1")
	assert.Zero(t, code, "deploy from inside: %s", truncate(out, 300))
	assert.Contains(t, out, "reloaded", "deploy reports what it did")
	e.waitForBody(fmt.Sprint(app["url"]), marker)
}

// TestBrowserTerminalDeliversAShell proves the terminal websocket end to end:
// the SSH banner arrives (the login shell reached the socket and identified
// the app), a resize is survived, and a command typed into the terminal runs
// in the container and prints. Before the fix this died before the banner,
// with runuser failing on control's host.
func TestBrowserTerminalDeliversAShell(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-term")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	ctx, cancel := context.WithTimeout(context.Background(), appLive)
	defer cancel()

	// The same endpoint the browser uses, authenticated the API way. Right
	// after create the app is still provisioning, and a dial in that window can
	// ACCEPT and then close before the shell says anything -- so the whole
	// attempt (dial, read to the banner) retries as one unit, the way the
	// browser's own reconnect loop does.
	wsURL := strings.Replace(e.host, "http", "ws", 1) + "/api/apps/" + name + "/terminal"
	var conn *websocket.Conn
	var transcript string
	require.Eventually(t, func() bool {
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: map[string][]string{"Authorization": {"Bearer " + e.token}},
		})
		if err != nil {
			return false
		}
		c.SetReadLimit(1 << 20)
		// The banner is the proof the whole chain worked: pty on the app's
		// node, login shell, Self()+Ensure() over the app socket.
		got, err := readUntil(ctx, c, "inside the container")
		if err != nil {
			_ = c.Close(websocket.StatusNormalClosure, "retry")
			transcript = got // keep the last evidence for the failure message
			return false
		}
		conn, transcript = c, got
		return true
	}, appLive, pollInterval, "the terminal never delivered the banner; last transcript: %s", truncate(transcript, 600))
	defer conn.Close(websocket.StatusNormalClosure, "done")
	assert.NotContains(t, transcript, "does not exist",
		"the shell must run on the app's node, where its user exists")
	assert.NotContains(t, transcript, "cannot identify app",
		"the login shell must reach the app socket")

	// A resize is a text frame; it must reach the remote pty without being
	// mistaken for input (the in-band framing this asserts is hand-written).
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"cols":120,"rows":40}`)))

	// Type a command. Its output proves keystrokes flow browser -> node pty and
	// the CLI works from within the terminal's own shell.
	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte("hostit status 2>&1 | head -3\n")))
	transcript, err := readUntil(ctx, conn, "hostit-app@")
	require.NoError(t, err, "the typed command never answered; transcript: %s", truncate(transcript, 600))
	assert.NotContains(t, transcript, "cannot reach hostit daemon")
}

// readUntil drains frames until the wanted substring shows up, returning
// whatever arrived either way so the caller can retry or show the evidence.
func readUntil(ctx context.Context, conn *websocket.Conn, want string) (string, error) {
	var transcript strings.Builder
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return transcript.String(), err
		}
		transcript.Write(data)
		if strings.Contains(transcript.String(), want) {
			return transcript.String(), nil
		}
	}
	return transcript.String(), fmt.Errorf("never saw %q", want)
}
