package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
	// testProfileKey is a second, distinct key used to check profile-key propagation
	testProfileKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIi b@host"
)

func TestSyncKeysRewritesEveryAppOfTheOwner(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "one")
	createTestApp(t, m, "two")
	// A profile key added later must reach every app the user owns
	require.NoError(t, m.SyncKeys("one", []string{testProfileKey}))
	require.NoError(t, m.SyncKeys("two", []string{testProfileKey}))
	for _, name := range []string{"one", "two"} {
		require.Contains(t, ops.authorizedKeys[name], testProfileKey, "app %s must get the profile key", name)
		assert.Contains(t, ops.authorizedKeys[name], testPublicKey, "its own app key stays")
	}
}

func TestCreateApp(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	app, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	assert.Equal(t, "blog", app.Name)
	assert.Equal(t, 10000, app.Port)
	assert.Equal(t, []string{"blog"}, ops.createdUsers)
	assert.Equal(t, []string{testPublicKey}, ops.authorizedKeys["blog"])
	assert.Contains(t, ops.scaffolds["blog"], "hostit.yml")
	assert.Contains(t, ops.scaffolds["blog"], "README.md")
	stored, err := m.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 10000, stored.Port)
}

func TestCreateAppWithoutAnyKeys(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	// An app with no keys is normal: it is driven through the API, and SSH is
	// there for whoever adds a key to their profile later
	_, err := m.CreateApp("blog", nil)
	require.NoError(t, err)
	assert.Empty(t, ops.authorizedKeys["blog"], "hostit must not invent a key pair")
	stored, err := m.Store().AppKeys("blog")
	require.NoError(t, err)
	assert.Empty(t, stored)
}

func TestCreateAppInvalidName(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	for _, name := range []string{"", "Foo", "-x", "x-", "a_b", "a.b", "root", "hostit", "api", strings.Repeat("x", 33)} {
		_, err := m.CreateApp(name, &CreateOptions{RequestKeys: []string{testPublicKey}})
		require.Error(t, err, "name %q should be rejected", name)
	}
}

func TestCreateAppDuplicate(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	_, err = m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.ErrorIs(t, err, ErrAppExists)
}

func TestCreateAppExistingUnixUser(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	ops.existingUsers = []string{"phil"}
	_, err := m.CreateApp("phil", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.ErrorIs(t, err, ErrAppExists)
}

func TestCreateAppInvalidSSHKey(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{"not a key"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh key")
}

func TestCreateAppPortAllocation(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	app1, err := m.CreateApp("one", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	app2, err := m.CreateApp("two", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	assert.Equal(t, 10000, app1.Port)
	assert.Equal(t, 10001, app2.Port)
}

func TestCreateAppPortRangeExhausted(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	m.config.PortMax = 10000 // Only one port available
	_, err := m.CreateApp("one", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	_, err = m.CreateApp("two", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.ErrorIs(t, err, ErrNoPortsAvailable)
}

func TestCreateAppUserCreationFails(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	ops.createUserErr = assert.AnError
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.Error(t, err)
	_, err = m.App("blog") // Nothing must be registered
	require.ErrorIs(t, err, store.ErrAppNotFound)
}

func TestDeleteApp(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	require.NoError(t, m.DeleteApp("blog"))
	assert.Equal(t, []string{"blog"}, ops.deletedUsers)
	_, err = m.App("blog")
	require.ErrorIs(t, err, store.ErrAppNotFound)
	require.ErrorIs(t, m.DeleteApp("blog"), store.ErrAppNotFound)
}

func TestSetKeys(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	otherKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL other@host"
	require.NoError(t, m.SetKeys("blog", []string{otherKey}, nil))
	assert.Equal(t, []string{otherKey}, ops.authorizedKeys["blog"])
	require.Error(t, m.SetKeys("blog", []string{"garbage"}, nil))
	require.ErrorIs(t, m.SetKeys("nope", []string{otherKey}, nil), store.ErrAppNotFound)
}

func TestApps(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	apps, err := m.Apps()
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
}

// newTestManagerDeps builds the config, store and fake ops a test Manager needs, so
// callers can construct the Manager via NewManager with their own runner (keeping
// every runner-backed service -- btrfs, etc. -- on that one runner).
func newTestManagerDeps(t *testing.T) (*config.Config, *store.Store, *fakeSystemOps) {
	t.Helper()
	conf := config.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = "secr3t"
	conf.AppsDir = t.TempDir()
	conf.DataDir = t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	return conf, s, newFakeSystemOps()
}

func newTestManager(t *testing.T) (*Manager, *fakeSystemOps) {
	t.Helper()
	conf, s, ops := newTestManagerDeps(t)
	return NewManager(conf, s, ops, newFakeRunner()), ops
}

// fakeSystemOps records system calls instead of executing them
type fakeSystemOps struct {
	existingUsers  []string
	createdUsers   []string
	deletedUsers   []string
	renamedUsers   []string
	killedUsers    []string
	authorizedKeys map[string][]string
	scaffolds      map[string][]string
	uids           map[string]int
	userHomes      map[string]string
	portRules      [][]PortRule
	createUserErr  error

	mu sync.Mutex // Protects everything above: CreateApp starts the app in the background
}

var _ SystemOps = (*fakeSystemOps)(nil)

func newFakeSystemOps() *fakeSystemOps {
	return &fakeSystemOps{
		authorizedKeys: make(map[string][]string),
		scaffolds:      make(map[string][]string),
		uids:           make(map[string]int),
		userHomes:      make(map[string]string),
	}
}

func (f *fakeSystemOps) LookupIDs(username string) (IDs, error) {
	uid, _ := f.LookupUID(username)
	return IDs{UID: uid, GID: uid, Count: 65536}, nil
}

func (f *fakeSystemOps) UserExists(username string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range append(f.existingUsers, f.createdUsers...) {
		if u == username {
			return true
		}
	}
	return false
}

func (f *fakeSystemOps) LookupUID(username string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if uid, ok := f.uids[username]; ok {
		return uid, nil
	}
	return 1001, nil
}

func (f *fakeSystemOps) CreateUser(username, home string, uid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createUserErr != nil {
		return f.createUserErr
	}
	f.createdUsers = append(f.createdUsers, username)
	f.uids[username] = uid
	return nil
}

func (f *fakeSystemOps) RemapUser(username, home string, uid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uids[username] = uid
	return nil
}

func (f *fakeSystemOps) SetUserHome(username, home string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userHomes[username] = home
	return nil
}

func (f *fakeSystemOps) KillUserProcesses(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killedUsers = append(f.killedUsers, username)
	return nil
}

// RenameUser moves all the fake's per-user state from the old login name to the
// new one, so lookups keep working after a rename just as the real usermod -l does.
func (f *fakeSystemOps) RenameUser(oldName, newName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renamedUsers = append(f.renamedUsers, oldName+"->"+newName)
	if uid, ok := f.uids[oldName]; ok {
		f.uids[newName] = uid
		delete(f.uids, oldName)
	}
	for i, u := range f.createdUsers {
		if u == oldName {
			f.createdUsers[i] = newName
		}
	}
	return nil
}

// setUID forces a user's uid, to simulate an app created before the block scheme
func (f *fakeSystemOps) setUID(username string, uid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uids[username] = uid
}

func (f *fakeSystemOps) DeleteUser(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedUsers = append(f.deletedUsers, username)
	return nil
}

func (f *fakeSystemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizedKeys[username] = keys
	return nil
}

// WriteScaffold records the scaffold AND writes it, so tests that read app
// files (README, hostit.yml) see what a real app would have
func (f *fakeSystemOps) WriteScaffold(username, home string, files map[string]string) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		f.scaffolds[username] = append(f.scaffolds[username], name)
		full := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSystemOps) ChownToUserIn(root *os.Root, username, rel string) error {
	return nil
}

func (f *fakeSystemOps) ApplyPortRules(rules []PortRule) error {
	f.portRules = append(f.portRules, rules)
	return nil
}
