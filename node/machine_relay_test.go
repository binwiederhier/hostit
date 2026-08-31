package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/run"
	"heckel.io/hostit/system/ssh"
	"heckel.io/hostit/system/systemd"
	"heckel.io/hostit/system/unixuser"
)

// recordingUser tracks stub create/delete and reports the current accounts.
type recordingUser struct {
	stubs   map[string]string // name -> home
	created []string
	deleted []string
}

func (u *recordingUser) Exists(name string) bool { _, ok := u.stubs[name]; return ok }
func (u *recordingUser) List() ([]unixuser.Account, error) {
	var out []unixuser.Account
	for n, h := range u.stubs {
		out = append(out, unixuser.Account{Name: n, Home: h})
	}
	return out, nil
}
func (u *recordingUser) LookupUID(string) (int, error)      { return 1001, nil }
func (u *recordingUser) LookupIDs(string) (int, int, error) { return 1001, 1001, nil }
func (u *recordingUser) Create(string, string, int) error   { return nil }
func (u *recordingUser) CreateStub(name, home string) error {
	u.stubs[name] = home
	u.created = append(u.created, name)
	return nil
}
func (u *recordingUser) Rename(string, string) error { return nil }
func (u *recordingUser) KillProcesses(string) error  { return nil }
func (u *recordingUser) Delete(name string) error {
	delete(u.stubs, name)
	u.deleted = append(u.deleted, name)
	return nil
}
func (u *recordingUser) WriteSkeleton(string, map[string]string) error { return nil }

// relayTestPaths points the frontend relay paths at a temp dir and marks the
// node a frontend (writes the relay pubkey), restoring the package vars after.
func relayTestPaths(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	saved := []*string{&relayRoutesPath, &relayKnownHostsPath, &relayKeysPath, &relayStubsPath, &relayPubKeyPath}
	orig := make([]string, len(saved))
	for i, p := range saved {
		orig[i] = *p
	}
	t.Cleanup(func() {
		for i, p := range saved {
			*p = orig[i]
		}
	})
	relayRoutesPath = filepath.Join(base, "ssh-routes")
	relayKnownHostsPath = filepath.Join(base, "relay_known_hosts")
	relayKeysPath = filepath.Join(base, "relay-keys")
	relayStubsPath = filepath.Join(base, "relay-stubs")
	relayPubKeyPath = filepath.Join(base, "relay_key.pub")
	require.NoError(t, os.WriteFile(relayPubKeyPath, []byte("ssh-ed25519 FRONTEND relay\n"), 0644))
}

func newRelayTestMachine(t *testing.T, u unixuser.Interface) *Machine {
	t.Helper()
	conf := NewConfig()
	conf.DataDir, conf.AppsDir = t.TempDir(), t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewMachine(conf, s, &Services{
		Runner: run.Nop{}, Btrfs: btrfs.New(run.Nop{}), Systemd: systemd.New(run.Nop{}),
		Container: podman.New(run.Nop{}), User: u, SSH: ssh.New(), Firewall: nopFirewall{},
	})
}

// TestApplyRelay drives the push path control now uses instead of shared files:
// the spec arrives over the link, the node writes the local artifacts the relay
// helper reads and reconciles the frontend stubs.
func TestApplyRelay(t *testing.T) {
	relayTestPaths(t)
	u := &recordingUser{stubs: map[string]string{}}
	m := newRelayTestMachine(t, u)

	// Two routed apps pushed with their keys.
	spec := &api.RelaySpec{
		Routes:     "shop\tnode2.ssh.example.com\nwiki\tnode2.ssh.example.com\n",
		KnownHosts: "node2.ssh.example.com ssh-ed25519 HOSTKEY\n",
		AppKeys: map[string]string{
			"shop": "ssh-ed25519 AAAA shopper\n",
			"wiki": "ssh-ed25519 BBBB wikier\n",
		},
	}
	require.NoError(t, m.ApplyRelay(spec))

	// The helper's files were written locally.
	routes, _ := os.ReadFile(relayRoutesPath)
	require.Equal(t, spec.Routes, string(routes))
	kh, _ := os.ReadFile(relayKnownHostsPath)
	require.Equal(t, spec.KnownHosts, string(kh))

	// A stub per routed app, serving that app's user keys.
	require.ElementsMatch(t, []string{"shop", "wiki"}, u.created, "a stub per routed app")
	ak, err := os.ReadFile(filepath.Join(relayStubsPath, "shop", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	require.Equal(t, "ssh-ed25519 AAAA shopper\n", string(ak))

	// Revoke: shop's keys emptied -> stub authorized_keys emptied.
	spec.AppKeys["shop"] = ""
	require.NoError(t, m.ApplyRelay(spec))
	ak, err = os.ReadFile(filepath.Join(relayStubsPath, "shop", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	require.Equal(t, "", string(ak), "revoked keys disappear from the frontend gate")

	// wiki dropped from the spec -> its stub and keys file are removed.
	spec.Routes = "shop\tnode2.ssh.example.com\n"
	delete(spec.AppKeys, "wiki")
	require.NoError(t, m.ApplyRelay(spec))
	require.Contains(t, u.deleted, "wiki")
	require.NotContains(t, u.deleted, "shop")
	_, err = os.Stat(filepath.Join(relayKeysPath, "wiki"))
	require.True(t, os.IsNotExist(err), "the dropped app's keys file is pruned")
}

// TestApplyRelayNonFrontend confirms a node without a relay key ignores a push.
func TestApplyRelayNonFrontend(t *testing.T) {
	relayTestPaths(t)
	require.NoError(t, os.Remove(relayPubKeyPath)) // not a frontend
	u := &recordingUser{stubs: map[string]string{}}
	m := newRelayTestMachine(t, u)
	require.NoError(t, m.ApplyRelay(&api.RelaySpec{Routes: "shop\tn\n", AppKeys: map[string]string{"shop": "k\n"}}))
	require.Empty(t, u.created, "a non-frontend node applies nothing")
}
