package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureNodeUpsertsWithoutToken(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// The colocated "local" node exists implicitly: joined from the start, no
	// token; ensure-style so every control start can call it.
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	n, err := s.Node("local")
	require.NoError(t, err)
	assert.False(t, n.JoinedAt.IsZero())

	nodes, err := s.Nodes()
	require.NoError(t, err)
	require.Len(t, nodes, 1)
}

func TestNodeSeenAndRemove(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	seen := time.Now()
	require.NoError(t, s.SetNodeSeen("local", seen))
	n, err := s.Node("local")
	require.NoError(t, err)
	assert.WithinDuration(t, seen, n.LastSeen, time.Second)

	require.NoError(t, s.RemoveNode("local"))
	_, err = s.Node("local")
	require.ErrorIs(t, err, ErrNodeNotFound)
}

func TestNodeSSHHostRoundTrip(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.EnsureNode("worker", "10.111.32.4"))

	// A node that has reported no SSH host has an empty one.
	n, err := s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "", n.SSHHost)

	// Once recorded, it round-trips, and re-recording overwrites it.
	require.NoError(t, s.SetNodeSSHHost("worker", "node2.ssh.example.com"))
	n, err = s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "node2.ssh.example.com", n.SSHHost)

	require.NoError(t, s.SetNodeSSHHost("worker", "moved.example.com"))
	n, err = s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "moved.example.com", n.SSHHost)

	// EnsureNode (an upsert on reconnect) updates the address without wiping the
	// SSH host a node reported separately.
	require.NoError(t, s.EnsureNode("worker", "10.111.32.9"))
	n, err = s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "moved.example.com", n.SSHHost)
}

func TestNodeHostKeyRoundTrip(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.EnsureNode("worker", "10.111.32.4"))
	n, err := s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "", n.HostKey)

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISOMEKEY node2"
	require.NoError(t, s.SetNodeHostKey("worker", key))
	n, err = s.Node("worker")
	require.NoError(t, err)
	require.Equal(t, key, n.HostKey)
}
