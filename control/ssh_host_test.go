package control

import (
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// An app on a node that reported an SSH host is advertised on THAT host; an app
// on the colocated node (or any node with no reported host) falls back to the
// control base domain, so a single-node deploy needs no configuration.
func TestSSHHostForUsesNodeReportedHost(t *testing.T) {
	s := newTestServer(t)
	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("worker", "10.111.32.4"))
	require.NoError(t, st.SetNodeSSHHost("worker", "node2.ssh.example.com"))

	require.Equal(t, "node2.ssh.example.com", s.sshHostFor("worker"))
	// base domain fallbacks
	require.Equal(t, "apps.example.com", s.sshHostFor(store.HostLocal))
	require.Equal(t, "apps.example.com", s.sshHostFor(""))
	require.Equal(t, "apps.example.com", s.sshHostFor("nonexistent-node"))
}

// A node reports its SSH host in the heartbeat; control records it against the
// node so the advertise path can find it.
func TestRecordNodeStatusStoresSSHHost(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.RecordNodeStatus("worker", &nodeapi.Heartbeat{
		Address: "10.111.32.4",
		SSHHost: "node2.ssh.example.com",
	})
	require.NoError(t, err)

	n, err := m.store.Node("worker")
	require.NoError(t, err)
	require.Equal(t, "node2.ssh.example.com", n.SSHHost)
}
