package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelaySSHArgs(t *testing.T) {
	// Interactive: a tty and no command.
	got := relaySSHArgs("node2.example.com", "shop", nil)
	require.Equal(t, "shop@node2.example.com", got[len(got)-1])
	require.Contains(t, got, "-tt")
	require.Contains(t, got, relayKeyFile)
	require.Contains(t, got, "StrictHostKeyChecking=yes")

	// Exec / rsync / scp: the command is passed as the remote command, no tty.
	got = relaySSHArgs("node2.example.com", "shop", []string{"-c", "rsync --server ."})
	require.Equal(t, "rsync --server .", got[len(got)-1])
	require.Equal(t, "shop@node2.example.com", got[len(got)-2])
	require.NotContains(t, got, "-tt")

	// sftp subsystem: requested with -s.
	got = relaySSHArgs("node2.example.com", "shop", []string{"-s", "sftp"})
	require.Equal(t, "sftp", got[len(got)-1])
	require.Equal(t, "shop@node2.example.com", got[len(got)-2])
	require.Contains(t, got, "-s")
}

func TestRelayBackend(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "ssh-routes")
	require.NoError(t, os.WriteFile(routes, []byte("shop\tnode2.example.com\nwiki\tnode3.example.com\n"), 0644))

	require.Equal(t, "node2.example.com", relayBackend(routes, "shop"))
	require.Equal(t, "node3.example.com", relayBackend(routes, "wiki"))
	require.Equal(t, "", relayBackend(routes, "blog"), "a colocated app has no route")
	require.Equal(t, "", relayBackend(filepath.Join(dir, "nope"), "shop"), "no routes file -> local")
	// A backend must never be derived from a substring/partial match.
	require.Equal(t, "", relayBackend(routes, "sho"))
}

func TestRelayInvocationCommandStaysAfterHost(t *testing.T) {
	// Guard: the remote command must come AFTER the host so ssh never parses a
	// command starting with "-" as an ssh option.
	got := relaySSHArgs("h", "app", []string{"-c", "-weird-cmd"})
	hostIdx, cmdIdx := -1, -1
	for i, a := range got {
		if a == "app@h" {
			hostIdx = i
		}
		if a == "-weird-cmd" {
			cmdIdx = i
		}
	}
	require.Greater(t, cmdIdx, hostIdx, "command comes after the host")
	require.Equal(t, cmdIdx, len(got)-1)
	_ = strings.Join(got, " ")
}
