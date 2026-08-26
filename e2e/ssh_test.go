//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SSH login is advertised on every app, and the browser terminal opens over
// a WebSocket. This is the e2e of "what does the SSH thing look like, and does
// the terminal work" -- the same flow driven by hand on stage. The multi-node
// relay and per-node-hostname paths need two nodes, so they stay unit-tested and
// stage-validated; this covers what a single live server can prove.
func TestAppAdvertisesSSHAndTerminalUpgrades(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := uniqueName("e2e-ssh")
	e.createApp(name)
	t.Cleanup(func() { e.deleteApp(name) })

	// The SSH panel: a ready-made ssh command and the host it lands on.
	var got map[string]any
	e.get("/api/apps/"+name, e.token, &got)
	ssh, ok := got["ssh"].(map[string]any)
	require.True(t, ok, "the app response carries an ssh block")
	host := fmt.Sprint(ssh["host"])
	assert.NotEmpty(t, host, "an advertised SSH host")
	assert.Equal(t, "ssh "+name+"@"+host, ssh["command"], "the ready-made ssh command")

	// The agent /info response inlines the same command.
	var info map[string]any
	e.get(fmt.Sprintf("/api/apps/%s/info", name), e.token, &info)
	if infoSSH, ok := info["ssh"].(map[string]any); ok {
		assert.Equal(t, ssh["command"], infoSSH["command"], "info shows the same ssh command")
	}

	// The browser terminal: a WebSocket that upgrades (101) and streams the pty.
	// Also the regression test for the metrics wrapper dropping http.Hijacker,
	// which turned this into a 501.
	wsURL := "ws" + strings.TrimPrefix(e.host, "http") + fmt.Sprintf("/api/apps/%s/terminal", name)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var conn *websocket.Conn
	var err error
	// Ensure() starts the container on connect; give a freshly created app a few tries.
	for i := 0; i < 20; i++ {
		conn, _, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + e.token}},
		})
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, err, "the terminal WebSocket must upgrade")
	defer conn.Close(websocket.StatusNormalClosure, "")

	// The pty streams at least one message once the shell is up.
	rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
	defer rcancel()
	_, data, err := conn.Read(rctx)
	require.NoError(t, err, "the terminal streams pty output")
	assert.NotEmpty(t, data)
}
