package control

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestSSHRelayFiles(t *testing.T) {
	apps := []*store.App{
		{Name: "blog", Host: store.HostLocal}, // colocated -> no route
		{Name: "shop", Host: "node2"},         // remote -> routed
		{Name: "wiki", Host: "node2"},         // same node
		{Name: "api", Host: "node3"},          // node3 has no host key yet
		{Name: "orphan", Host: "ghost"},       // node not in table -> skipped
	}
	nodes := map[string]*store.Node{
		"node2": {Name: "node2", SSHHost: "node2.ssh.example.com", HostKey: "ssh-ed25519 AAA node2"},
		"node3": {Name: "node3", SSHHost: "node3.ssh.example.com", HostKey: ""}, // routed but no kh line
	}
	routes, kh := sshRelayFiles(apps, nodes)

	require.Equal(t,
		"api\tnode3.ssh.example.com\nshop\tnode2.ssh.example.com\nwiki\tnode2.ssh.example.com\n",
		routes, "one sorted route per remote app whose node reports an ssh host")
	require.Equal(t,
		"node2.ssh.example.com ssh-ed25519 AAA node2\n",
		kh, "one known_hosts line per distinct remote node that has a host key")
}

func TestWriteSSHRelayFilesRoundTrip(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	s.config.SSHRelayEnabled = true
	s.config.SSHRelayRoutesFile = dir + "/ssh-routes"
	s.config.SSHRelayKnownHostsFile = dir + "/relay_known_hosts"

	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("node2", "10.0.0.2"))
	require.NoError(t, st.SetNodeSSHHost("node2", "node2.ssh.example.com"))
	require.NoError(t, st.SetNodeHostKey("node2", "ssh-ed25519 AAA node2"))
	require.NoError(t, st.AddApp(&store.App{Name: "shop", Port: 12001, Host: "node2"}))
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 12002, Host: store.HostLocal}))

	require.NoError(t, s.apps.WriteSSHRelayFiles())

	routes, err := os.ReadFile(dir + "/ssh-routes")
	require.NoError(t, err)
	require.Equal(t, "shop\tnode2.ssh.example.com\n", string(routes))
	kh, err := os.ReadFile(dir + "/relay_known_hosts")
	require.NoError(t, err)
	require.Equal(t, "node2.ssh.example.com ssh-ed25519 AAA node2\n", string(kh))

	// Disabled -> no write (stale/removed handled by ansible, not here).
	s.config.SSHRelayEnabled = false
	require.NoError(t, s.apps.WriteSSHRelayFiles())
}
