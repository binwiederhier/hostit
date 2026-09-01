package relay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUsers records the root user ops instead of running them; ensureStub still
// creates the on-disk home + keys, which the reconcile owns.
type fakeUsers struct {
	exists   map[string]bool
	created  []string
	deleted  []string
	killed   []string
	accounts []Account
}

func newFakeUsers() *fakeUsers { return &fakeUsers{exists: map[string]bool{}} }

func (f *fakeUsers) Exists(name string) bool { return f.exists[name] }
func (f *fakeUsers) CreateStub(name, home string) error {
	f.created = append(f.created, name)
	f.exists[name] = true
	f.accounts = append(f.accounts, Account{Name: name, Home: home})
	return nil
}
func (f *fakeUsers) Delete(name string) error {
	f.deleted = append(f.deleted, name)
	f.exists[name] = false
	return nil
}
func (f *fakeUsers) KillProcesses(name string) error { f.killed = append(f.killed, name); return nil }
func (f *fakeUsers) List() ([]Account, error)        { return f.accounts, nil }

func tempPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Routes:     filepath.Join(dir, "ssh-routes"),
		KnownHosts: filepath.Join(dir, "relay_known_hosts"),
		Keys:       filepath.Join(dir, "relay-keys"),
		Stubs:      filepath.Join(dir, "relay-stubs"),
		Key:        filepath.Join(dir, "relay_key"),
		PubKey:     filepath.Join(dir, "relay_key.pub"),
	}
}

func TestApplyCreatesStubForRoutedApp(t *testing.T) {
	t.Parallel()
	users := newFakeUsers()
	p := tempPaths(t)
	s := New(users, p)

	spec := &Spec{
		Routes:  "blog\tnode1.example.com\n",
		AppKeys: map[string]string{"blog": "ssh-ed25519 AAAA user@host\n"},
	}
	require.NoError(t, s.Apply(spec))

	assert.Equal(t, []string{"blog"}, users.created, "the routed app gets a stub account")
	// The stub's authorized_keys is written under the stubs home, root-owned.
	ak, err := os.ReadFile(filepath.Join(p.Stubs, "blog", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519 AAAA user@host\n", string(ak))
}

func TestApplyRemovesStaleStub(t *testing.T) {
	t.Parallel()
	users := newFakeUsers()
	p := tempPaths(t)
	// A stub for an app that is no longer routed, homed under the stubs dir.
	users.exists["old"] = true
	users.accounts = append(users.accounts, Account{Name: "old", Home: filepath.Join(p.Stubs, "old")})
	s := New(users, p)

	require.NoError(t, s.Apply(&Spec{})) // empty spec: nothing routed
	assert.Equal(t, []string{"old"}, users.deleted, "a no-longer-routed stub is removed")
	assert.Equal(t, []string{"old"}, users.killed, "its processes are killed first")
}

func TestApplyNeverTouchesNonStubAccounts(t *testing.T) {
	t.Parallel()
	users := newFakeUsers()
	p := tempPaths(t)
	// A real account homed OUTSIDE the stubs dir (e.g. a system user) must never
	// be deleted, even though it is not routed.
	users.exists["postgres"] = true
	users.accounts = append(users.accounts, Account{Name: "postgres", Home: "/var/lib/postgresql"})
	s := New(users, p)

	require.NoError(t, s.Apply(&Spec{}))
	assert.Empty(t, users.deleted, "a non-stub account is off-limits to the reconcile")
}

func TestApplyIgnoresInvalidRoutedName(t *testing.T) {
	t.Parallel()
	users := newFakeUsers()
	p := tempPaths(t)
	s := New(users, p)
	// An unexpected name in the routes must never reach a useradd.
	require.NoError(t, s.Apply(&Spec{Routes: "Bad_Name\tnode1\n"}))
	assert.Empty(t, users.created)
}

func TestEnsureKeyGeneratesOnceAndReports(t *testing.T) {
	t.Parallel()
	users := newFakeUsers()
	p := tempPaths(t)
	s := New(users, p)

	pub1, err := s.EnsureKey()
	require.NoError(t, err)
	require.NotEmpty(t, pub1)
	assert.Contains(t, pub1, "ssh-ed25519")
	// The private key exists and is root-only.
	info, err := os.Stat(p.Key)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Idempotent: a second call does not regenerate, and reports the same key.
	pub2, err := s.EnsureKey()
	require.NoError(t, err)
	assert.Equal(t, pub1, pub2)
}
