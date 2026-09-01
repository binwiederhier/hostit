package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/system/relaypaths"
)

func TestRelayBackendResolvesRoutedApp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	routes := filepath.Join(dir, "ssh-routes")
	require.NoError(t, os.WriteFile(routes, []byte("blog\tnode1.example.com\nwiki\tnode2.example.com\n"), 0644))

	assert.Equal(t, "node1.example.com", relayBackend(routes, "blog"))
	assert.Equal(t, "node2.example.com", relayBackend(routes, "wiki"))
	assert.Equal(t, "", relayBackend(routes, "unknown"), "an unrouted app has no backend")
	assert.Equal(t, "", relayBackend(filepath.Join(dir, "missing"), "blog"), "a missing routes file relays nothing")
}

func TestRelaySSHArgsByInvocation(t *testing.T) {
	t.Parallel()
	// Interactive shell (no command): request a tty.
	got := relaySSHArgs("node1", "blog", nil)
	assert.Contains(t, got, "-i")
	assert.Contains(t, got, relaypaths.Key)
	assert.Contains(t, got, "blog@node1")
	assert.Contains(t, got, "-tt")

	// exec/rsync/scp: "-c <cmd>" is passed as the remote command, no tty.
	got = relaySSHArgs("node1", "blog", []string{"-c", "ls -la"})
	assert.Equal(t, "ls -la", got[len(got)-1])
	assert.NotContains(t, got, "-tt")

	// sftp subsystem: "-s sftp" is requested as a subsystem.
	got = relaySSHArgs("node1", "blog", []string{"-s", "sftp"})
	assert.Contains(t, got, "-s")
	assert.Equal(t, "sftp", got[len(got)-1])
}
