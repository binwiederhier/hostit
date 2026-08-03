package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
)

func TestCreateApp(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	app, creds, err := m.CreateApp("blog", []string{testPublicKey})
	require.NoError(t, err)
	assert.Equal(t, "blog", app.Name)
	assert.Equal(t, 10000, app.Port)
	assert.Nil(t, creds) // Key was provided, none generated
	assert.Equal(t, []string{"blog"}, ops.createdUsers)
	assert.Equal(t, []string{"blog"}, ops.lingerEnabled)
	assert.Equal(t, []string{testPublicKey}, ops.authorizedKeys["blog"])
	assert.Contains(t, ops.scaffolds["blog"], "hostit.yml")
	assert.Contains(t, ops.scaffolds["blog"], "README.txt")
	stored, err := m.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 10000, stored.Port)
}

func TestCreateAppGeneratesKeyPair(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	_, creds, err := m.CreateApp("blog", nil)
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Contains(t, creds.PrivateKey, "OPENSSH PRIVATE KEY")
	require.Len(t, ops.authorizedKeys["blog"], 1)
	// The written public key must match the returned private key
	signer, err := ssh.ParsePrivateKey([]byte(creds.PrivateKey))
	require.NoError(t, err)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(ops.authorizedKeys["blog"][0]))
	require.NoError(t, err)
	assert.Equal(t, string(pub.Marshal()), string(signer.PublicKey().Marshal()))
}

func TestCreateAppInvalidName(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	for _, name := range []string{"", "Foo", "-x", "x-", "a_b", "a.b", "root", "hostit", "api", strings.Repeat("x", 33)} {
		_, _, err := m.CreateApp(name, []string{testPublicKey})
		require.Error(t, err, "name %q should be rejected", name)
	}
}

func TestCreateAppDuplicate(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, _, err := m.CreateApp("blog", []string{testPublicKey})
	require.NoError(t, err)
	_, _, err = m.CreateApp("blog", []string{testPublicKey})
	require.ErrorIs(t, err, ErrAppExists)
}

func TestCreateAppExistingUnixUser(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	ops.existingUsers = []string{"phil"}
	_, _, err := m.CreateApp("phil", []string{testPublicKey})
	require.ErrorIs(t, err, ErrAppExists)
}

func TestCreateAppInvalidSSHKey(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, _, err := m.CreateApp("blog", []string{"not a key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh key")
}

func TestCreateAppPortAllocation(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	app1, _, err := m.CreateApp("one", []string{testPublicKey})
	require.NoError(t, err)
	app2, _, err := m.CreateApp("two", []string{testPublicKey})
	require.NoError(t, err)
	assert.Equal(t, 10000, app1.Port)
	assert.Equal(t, 10001, app2.Port)
}

func TestCreateAppPortRangeExhausted(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	m.config.PortMax = 10000 // Only one port available
	_, _, err := m.CreateApp("one", []string{testPublicKey})
	require.NoError(t, err)
	_, _, err = m.CreateApp("two", []string{testPublicKey})
	require.ErrorIs(t, err, ErrNoPortsAvailable)
}

func TestCreateAppUserCreationFails(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	ops.createUserErr = assert.AnError
	_, _, err := m.CreateApp("blog", []string{testPublicKey})
	require.Error(t, err)
	_, err = m.App("blog") // Nothing must be registered
	require.ErrorIs(t, err, store.ErrAppNotFound)
}

func TestDeleteApp(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	_, _, err := m.CreateApp("blog", []string{testPublicKey})
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
	_, _, err := m.CreateApp("blog", []string{testPublicKey})
	require.NoError(t, err)
	otherKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL other@host"
	require.NoError(t, m.SetKeys("blog", []string{otherKey}))
	assert.Equal(t, []string{otherKey}, ops.authorizedKeys["blog"])
	require.Error(t, m.SetKeys("blog", []string{"garbage"}))
	require.ErrorIs(t, m.SetKeys("nope", []string{otherKey}), store.ErrAppNotFound)
}

func TestApps(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, _, err := m.CreateApp("blog", []string{testPublicKey})
	require.NoError(t, err)
	apps, err := m.Apps()
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
}

func newTestManager(t *testing.T) (*Manager, *fakeSystemOps) {
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
	ops := newFakeSystemOps()
	return NewManager(conf, s, ops, newFakeUserRunner()), ops
}

// fakeSystemOps records system calls instead of executing them
type fakeSystemOps struct {
	existingUsers  []string
	createdUsers   []string
	deletedUsers   []string
	lingerEnabled  []string
	authorizedKeys map[string][]string
	scaffolds      map[string][]string
	userFiles      map[string]string
	uids           map[string]int
	portRules      [][]PortRule
	createUserErr  error
}

var _ SystemOps = (*fakeSystemOps)(nil)

func newFakeSystemOps() *fakeSystemOps {
	return &fakeSystemOps{
		authorizedKeys: make(map[string][]string),
		scaffolds:      make(map[string][]string),
		userFiles:      make(map[string]string),
		uids:           make(map[string]int),
	}
}

func (f *fakeSystemOps) UserExists(username string) bool {
	for _, u := range append(f.existingUsers, f.createdUsers...) {
		if u == username {
			return true
		}
	}
	return false
}

func (f *fakeSystemOps) LookupUID(username string) (int, error) {
	if uid, ok := f.uids[username]; ok {
		return uid, nil
	}
	return 1001, nil
}

func (f *fakeSystemOps) CreateUser(username, home string) error {
	if f.createUserErr != nil {
		return f.createUserErr
	}
	f.createdUsers = append(f.createdUsers, username)
	f.uids[username] = 1000 + len(f.createdUsers)
	return nil
}

func (f *fakeSystemOps) DeleteUser(username string) error {
	f.deletedUsers = append(f.deletedUsers, username)
	return nil
}

func (f *fakeSystemOps) EnableLinger(username string) error {
	f.lingerEnabled = append(f.lingerEnabled, username)
	return nil
}

func (f *fakeSystemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	f.authorizedKeys[username] = keys
	return nil
}

func (f *fakeSystemOps) WriteScaffold(username, home string, files map[string]string) error {
	for name := range files {
		f.scaffolds[username] = append(f.scaffolds[username], name)
	}
	return nil
}

func (f *fakeSystemOps) WriteUserFile(username, home, relPath, content string, mode os.FileMode) error {
	f.userFiles[username+":"+relPath] = content
	return nil
}

func (f *fakeSystemOps) ApplyPortRules(rules []PortRule) error {
	f.portRules = append(f.portRules, rules)
	return nil
}
