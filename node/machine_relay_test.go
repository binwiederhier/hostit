package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
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

func newRelayTestMachine(t *testing.T, u unixuser.Interface) *Machine {
	t.Helper()
	conf := NewConfig()
	conf.DataDir, conf.AppsDir = t.TempDir(), t.TempDir()
	base := t.TempDir()
	conf.SSHRoutesFile = filepath.Join(base, "ssh-routes")
	conf.RelayKeysDir = filepath.Join(base, "relay-keys")
	conf.RelayStubsDir = filepath.Join(base, "relay-stubs")
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewMachine(conf, s, &Services{
		Runner: run.Nop{}, Btrfs: btrfs.New(run.Nop{}), Systemd: systemd.New(run.Nop{}),
		Container: podman.New(run.Nop{}), User: u, SSH: ssh.New(), Firewall: nopFirewall{},
	})
}

func TestReconcileRelayStubs(t *testing.T) {
	u := &recordingUser{stubs: map[string]string{}}
	m := newRelayTestMachine(t, u)
	require.NoError(t, os.MkdirAll(m.config.RelayKeysDir, 0755))
	write := func(p, c string) { require.NoError(t, os.WriteFile(p, []byte(c), 0644)) }

	// Two routed apps, each with a keys file.
	write(m.config.SSHRoutesFile, "shop\tnode2.ssh.example.com\nwiki\tnode2.ssh.example.com\n")
	write(filepath.Join(m.config.RelayKeysDir, "shop"), "ssh-ed25519 AAAA shopper\n")
	write(filepath.Join(m.config.RelayKeysDir, "wiki"), "ssh-ed25519 BBBB wikier\n")

	m.ReconcileRelayStubs()
	require.ElementsMatch(t, []string{"shop", "wiki"}, u.created, "a stub per routed app")
	ak, err := os.ReadFile(filepath.Join(m.config.RelayStubsDir, "shop", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	require.Equal(t, "ssh-ed25519 AAAA shopper\n", string(ak), "stub serves the app's user keys")

	// Revoke: shop's key file emptied -> stub authorized_keys emptied within a reconcile.
	write(filepath.Join(m.config.RelayKeysDir, "shop"), "")
	m.ReconcileRelayStubs()
	ak, err = os.ReadFile(filepath.Join(m.config.RelayStubsDir, "shop", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	require.Equal(t, "", string(ak), "revoked keys disappear from the frontend gate")

	// wiki removed from routes -> its stub is deleted.
	write(m.config.SSHRoutesFile, "shop\tnode2.ssh.example.com\n")
	m.ReconcileRelayStubs()
	require.Contains(t, u.deleted, "wiki")
	require.NotContains(t, u.deleted, "shop")
}
