package control

import (
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

// buildRelaySpec is what control pushes to a frontend node over the link: the
// routes, known_hosts and each remote app's authorized_keys.
func TestBuildRelaySpec(t *testing.T) {
	s := newTestServer(t)
	s.config.SSHRelayEnabled = true

	st := s.apps.Store()
	require.NoError(t, st.EnsureNode("node2", "10.0.0.2"))
	require.NoError(t, st.SetNodeSSHHost("node2", "node2.ssh.example.com"))
	require.NoError(t, st.SetNodeHostKey("node2", "ssh-ed25519 AAA node2"))
	require.NoError(t, st.AddApp(&store.App{Name: "shop", Port: 12001, Host: "node2"}))
	require.NoError(t, st.SetAppKeys("shop", []string{"ssh-ed25519 AAAA shopper-key"}))
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 12002, Host: store.HostLocal}))

	spec, err := s.apps.buildRelaySpec()
	require.NoError(t, err)
	require.Equal(t, "shop\tnode2.ssh.example.com\n", spec.Routes)
	require.Equal(t, "node2.ssh.example.com ssh-ed25519 AAA node2\n", spec.KnownHosts)
	// The remote app's keys are in the spec for its frontend stub; the colocated
	// app is not in the spec at all.
	require.Contains(t, spec.AppKeys["shop"], "shopper-key")
	_, ok := spec.AppKeys["blog"]
	require.False(t, ok, "colocated app is not relayed")
}

func TestDesiredStateInjectsRelayKeyForRemoteNodesOnly(t *testing.T) {
	s := newTestServer(t)
	pub := "ssh-ed25519 AAAARELAYPUBKEY hostit-relay"
	s.config.SSHRelayEnabled = true
	s.apps.recordRelayFrontend("local", pub) // frontend reported its relay key

	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{Name: "shop", Port: 13001, Host: "node2"}))
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 13002, Host: store.HostLocal}))
	require.NoError(t, st.SetAppKeys("shop", []string{"ssh-ed25519 AAAAUSER shopper"}))
	require.NoError(t, st.SetAppKeys("blog", []string{"ssh-ed25519 AAAAUSER blogger"}))

	relayLine := "restrict,pty " + pub

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

// relayKeyLine is empty until a frontend reports its pubkey, and gated by the
// relay being enabled (a stale line must not leak when the relay is off).
func TestRelayKeyLine(t *testing.T) {
	s := newTestServer(t)
	s.config.SSHRelayEnabled = true
	require.Equal(t, "", s.apps.relayKeyLine(), "no frontend has reported yet -> empty")

	s.apps.recordRelayFrontend("local", "ssh-ed25519 AAAAKEY hostit-relay")
	require.Equal(t, `restrict,pty ssh-ed25519 AAAAKEY hostit-relay`,
		s.apps.relayKeyLine(), "reported key is used")

	s.config.SSHRelayEnabled = false
	require.Equal(t, "", s.apps.relayKeyLine(), "off -> empty even with a reported key")
}

// The relay key must ride BOTH the mirror and the explicit SetKeys path (a key
// resync via SetKeys once dropped it on stage). appendRelayKey is the shared
// point; it adds the line for remote apps only, and only when the relay is on.
func TestAppendRelayKey(t *testing.T) {
	s := newTestServer(t)
	s.config.SSHRelayEnabled = true
	s.apps.recordRelayFrontend("local", "ssh-ed25519 AAAAK hostit-relay")

	user := []string{"ssh-ed25519 AAAAUSER u"}
	line := "restrict,pty ssh-ed25519 AAAAK hostit-relay"

	require.Equal(t, []string{"ssh-ed25519 AAAAUSER u", line}, s.apps.appendRelayKey("node2", user), "remote app gets it")
	require.Equal(t, user, s.apps.appendRelayKey(store.HostLocal, user), "colocated app never does")
	require.Equal(t, user, s.apps.appendRelayKey("", user), "unplaced app never does")

	s.config.SSHRelayEnabled = false
	require.Equal(t, user, s.apps.appendRelayKey("node2", user), "off -> never")
}

// recordRelayFrontend returns whether the relay line changed -- the signal that
// gates the one-time resync of remote nodes' keys. A regression here would drop
// the resync (or run it every heartbeat).
func TestRecordRelayFrontendChanged(t *testing.T) {
	s := newTestServer(t)
	require.True(t, s.apps.recordRelayFrontend("local", "ssh-ed25519 AAAA k"), "first report is a change")
	require.False(t, s.apps.recordRelayFrontend("local", "ssh-ed25519 AAAA k"), "same key is not a change")
	require.True(t, s.apps.recordRelayFrontend("local", "ssh-ed25519 BBBB k2"), "a new key is a change")
}
