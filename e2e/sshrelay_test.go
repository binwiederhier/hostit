//go:build e2e

package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

// TestSSHRelayReachesApp proves the single-hostname SSH path end to end: create
// an app with a known key, then actually `ssh <app>@<base-domain>` and run a
// command inside its container. On a split install this exercises the control
// frontend's relay (a remote app is reached through a stub + the forwarder); on
// a single box it is the direct login. Opt-in, because it needs a reachable
// base domain and the relay enabled -- run it against stage:
//
//	HOSTIT_HOST=https://stageapps.heckel.io HOSTIT_TOKEN=... HOSTIT_E2E_RELAY=1 make e2e RUN=TestSSHRelayReachesApp
func TestSSHRelayReachesApp(t *testing.T) {
	if os.Getenv("HOSTIT_E2E_RELAY") != "1" {
		t.Skip("HOSTIT_E2E_RELAY!=1: the ssh-relay e2e needs a relay-enabled, reachable env")
	}
	e := newEnv(t)
	name := uniqueName("e2e-relay")

	// A fresh keypair; the app authorizes the public half.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	sshPub, err := xssh.NewPublicKey(pub)
	require.NoError(t, err)
	authLine := strings.TrimSpace(string(xssh.MarshalAuthorizedKey(sshPub)))

	var app map[string]any
	e.doJSON("POST", "/api/apps", e.token,
		map[string]any{"name": name, "ssh_keys": []string{authLine}}, &app, http.StatusCreated)
	t.Cleanup(func() { e.deleteApp(name) })

	// The base domain is where the single-hostname relay listens.
	baseDomain := hostOnly(e.host)
	marker := "hostit-relay-ok-" + name

	cfg := &xssh.ClientConfig{
		User:            name,
		Auth:            []xssh.AuthMethod{xssh.PublicKeys(signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// The container starts on first connect (Ensure); give a fresh app a few tries.
	var out string
	var lastErr error
	for i := 0; i < 25; i++ {
		out, lastErr = sshRun(net.JoinHostPort(baseDomain, "22"), cfg, "echo "+marker)
		if lastErr == nil && strings.Contains(out, marker) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	require.NoError(t, lastErr, "ssh %s@%s must connect and run a command", name, baseDomain)
	assert.Contains(t, out, marker, "the command ran inside the app and returned its output")
}

// sshRun dials, runs one command, and returns its combined output.
func sshRun(addr string, cfg *xssh.ClientConfig, cmd string) (string, error) {
	client, err := xssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	defer sess.Close()
	b, err := sess.CombinedOutput(cmd)
	return string(b), err
}

// hostOnly strips the scheme and any port from a base URL, leaving the hostname.
func hostOnly(u string) string {
	h := u
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h = strings.TrimSuffix(h, "/")
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
