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
	s.config.SSHRelayKeysDir = dir + "/relay-keys"

	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("node2", "10.0.0.2"))
	require.NoError(t, st.SetNodeSSHHost("node2", "node2.ssh.example.com"))
	require.NoError(t, st.SetNodeHostKey("node2", "ssh-ed25519 AAA node2"))
	require.NoError(t, st.AddApp(&store.App{Name: "shop", Port: 12001, Host: "node2"}))
	require.NoError(t, st.SetAppKeys("shop", []string{"ssh-ed25519 AAAA shopper-key"}))
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 12002, Host: store.HostLocal}))

	require.NoError(t, s.apps.WriteSSHRelayFiles())

	routes, err := os.ReadFile(dir + "/ssh-routes")
	require.NoError(t, err)
	require.Equal(t, "shop\tnode2.ssh.example.com\n", string(routes))
	kh, err := os.ReadFile(dir + "/relay_known_hosts")
	require.NoError(t, err)
	require.Equal(t, "node2.ssh.example.com ssh-ed25519 AAA node2\n", string(kh))
	// The remote app's per-app keys file is written for its frontend stub; the
	// colocated app gets none.
	shopKeys, err := os.ReadFile(dir + "/relay-keys/shop")
	require.NoError(t, err)
	require.Contains(t, string(shopKeys), "shopper-key")
	_, err = os.Stat(dir + "/relay-keys/blog")
	require.True(t, os.IsNotExist(err), "colocated app has no relay keys file")

	// Disabled -> no write (stale/removed handled by ansible, not here).
	s.config.SSHRelayEnabled = false
	require.NoError(t, s.apps.WriteSSHRelayFiles())
}

func TestDesiredStateInjectsRelayKeyForRemoteNodesOnly(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	pub := "ssh-ed25519 AAAARELAYPUBKEY hostit-relay"
	require.NoError(t, os.WriteFile(dir+"/relay_key.pub", []byte(pub+"\n"), 0644))
	s.config.SSHRelayEnabled = true
	s.config.SSHRelayPublicKeyFile = dir + "/relay_key.pub"
	s.config.SSHRelayFromAddress = "10.111.32.3"

	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{Name: "shop", Port: 13001, Host: "node2"}))
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 13002, Host: store.HostLocal}))
	require.NoError(t, st.SetAppKeys("shop", []string{"ssh-ed25519 AAAAUSER shopper"}))
	require.NoError(t, st.SetAppKeys("blog", []string{"ssh-ed25519 AAAAUSER blogger"}))

	relayLine := `from="10.111.32.3",restrict,pty ` + pub

	// Remote node: the relay line is appended to the app's keys.
	remote, err := s.apps.DesiredState("node2")
	require.NoError(t, err)
	require.Len(t, remote.Apps, 1)
	require.Contains(t, remote.Apps[0].SSHKeys, relayLine, "remote node trusts the relay key")
	require.Contains(t, remote.Apps[0].SSHKeys, "ssh-ed25519 AAAAUSER shopper", "and still the user's own key")

	// Colocated node: never the relay line (no relay to itself).
	local, err := s.apps.DesiredState(store.HostLocal)
	require.NoError(t, err)
	require.Len(t, local.Apps, 1)
	require.NotContains(t, local.Apps[0].SSHKeys, relayLine, "colocated node does not get the relay key")
}

// Regression: relayKeyLine must not permanently cache an empty result. Control
// can start before the deploy generates relay_key.pub; caching empty would drop
// the relay key from every node until a restart (found live on stage).
func TestRelayKeyLineDoesNotCacheEmpty(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	s.config.SSHRelayEnabled = true
	s.config.SSHRelayPublicKeyFile = dir + "/relay_key.pub" // absent for now
	s.config.SSHRelayFromAddress = "10.0.0.1"

	require.Equal(t, "", s.apps.relayKeyLine(), "no key file yet -> empty")

	require.NoError(t, os.WriteFile(dir+"/relay_key.pub", []byte("ssh-ed25519 AAAAKEY hostit-relay\n"), 0644))
	require.Equal(t, `from="10.0.0.1",restrict,pty ssh-ed25519 AAAAKEY hostit-relay`,
		s.apps.relayKeyLine(), "key appears -> picked up without a restart")
}
