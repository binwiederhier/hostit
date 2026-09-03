package control

import (
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
)

// An app on a node that reported an SSH host is advertised on THAT host; an app
// on the colocated node (or any node with no reported host) falls back to the
// control base domain, so a single-node deploy needs no configuration.
func TestSSHHostForUsesNodeReportedHost(t *testing.T) {
	s := newTestServer(t)
	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("worker", "10.0.0.4"))
	require.NoError(t, st.SetNodeSSHHost("worker", "node2.ssh.example.com"))

	require.Equal(t, "node2.ssh.example.com", s.sshHostFor("worker"))
	// base domain fallbacks
	require.Equal(t, "apps.example.com", s.sshHostFor(store.HostLocal))
	require.Equal(t, "apps.example.com", s.sshHostFor(""))
	require.Equal(t, "apps.example.com", s.sshHostFor("nonexistent-node"))
}

// With the SSH relay on, the user is always sent to the control host (the base
// domain): the relay routes to the right node behind the scenes, and the node's
// own ssh-host is an internal detail that must never be advertised.
func TestSSHHostForRelayHidesNodeHost(t *testing.T) {
	s := newTestServer(t)
	s.config.SSHRelayEnabled = true
	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("worker", "10.0.0.4"))
	require.NoError(t, st.SetNodeSSHHost("worker", "node2.ssh.example.com"))

	require.Equal(t, "apps.example.com", s.sshHostFor("worker"), "a remote app is advertised on the control host, not the node")
	require.Equal(t, "apps.example.com", s.sshHostFor(store.HostLocal))
	require.Equal(t, "apps.example.com", s.sshHostFor(""))
}

// A node reports its SSH host in the heartbeat; control records it against the
// node so the advertise path can find it.
func TestRecordNodeStatusStoresSSHHost(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.RecordNodeStatus("worker", &api.Heartbeat{
		Address: "10.0.0.4",
		SSHHost: "node2.ssh.example.com",
	})
	require.NoError(t, err)

	n, err := m.store.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "node2.ssh.example.com", n.SSHHost)
}

// A node reports its sshd host key in the heartbeat; control records it so the
// relay gateway's known_hosts can be written.
func TestRecordNodeStatusStoresHostKey(t *testing.T) {
	m, _ := newTestManager(t)
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHOSTKEY node2"
	err := m.RecordNodeStatus("worker", &api.Heartbeat{Address: "10.0.0.4", SSHHostKey: key})
	require.NoError(t, err)
	n, err := m.store.Node("worker")
	require.NoError(t, err)
	require.Equal(t, key, n.HostKey)
}
