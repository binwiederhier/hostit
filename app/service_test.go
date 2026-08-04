package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
)

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
	assert.Contains(t, ops.scaffolds["blog"], "HOSTIT.txt")
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
	return NewManager(conf, s, ops, newFakeRunner()), ops
}

// fakeSystemOps records system calls instead of executing them
type fakeSystemOps struct {
	existingUsers  []string
	createdUsers   []string
	deletedUsers   []string
	authorizedKeys map[string][]string
	scaffolds      map[string][]string
	userFiles      map[string]string
	uids           map[string]int
	portRules      [][]PortRule
	images         map[string]bool
	builds         []imageBuild
	createUserErr  error
}

// imageBuild records a BuildImage call
type imageBuild struct {
	contextDir string
	tag        string
}

var _ SystemOps = (*fakeSystemOps)(nil)

func newFakeSystemOps() *fakeSystemOps {
	return &fakeSystemOps{
		authorizedKeys: make(map[string][]string),
		scaffolds:      make(map[string][]string),
		userFiles:      make(map[string]string),
		uids:           make(map[string]int),
		images:         make(map[string]bool),
	}
}

func (f *fakeSystemOps) ImageExists(tag string) bool {
	return f.images[tag]
}

func (f *fakeSystemOps) BuildImage(contextDir, tag string) error {
	f.builds = append(f.builds, imageBuild{contextDir: contextDir, tag: tag})
	f.images[tag] = true
	return nil
}

func (f *fakeSystemOps) LookupIDs(username string) (IDs, error) {
	uid, _ := f.LookupUID(username)
	return IDs{UID: uid, GID: uid, SubUID: 100000 + uid*65536, SubGID: 100000 + uid*65536, SubCount: 65536}, nil
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

func (f *fakeSystemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
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
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSystemOps) WriteUserFile(username, home, relPath, content string, mode os.FileMode) error {
	f.userFiles[username+":"+relPath] = content
	return nil
}

func (f *fakeSystemOps) ChownToUser(username, path string) error {
	return nil
}

func (f *fakeSystemOps) ApplyPortRules(rules []PortRule) error {
	f.portRules = append(f.portRules, rules)
	return nil
}
