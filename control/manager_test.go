package control

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/nftables"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/run"
	"heckel.io/hostit/system/ssh"
	"heckel.io/hostit/system/systemd"
	"heckel.io/hostit/system/unixuser"
	"heckel.io/hostit/workspace"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
	// testProfileKey is a second, distinct key used to check profile-key propagation
	testProfileKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIi b@host"
)

// A profile key added later reaches every app the user owns, and each app
// keeps its own keys: control resolves both halves and writes the full set
// (there is no verb that asks a node for keys it does not have).
func TestSetKeysRewritesEveryAppOfTheOwner(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "one")
	createTestApp(t, m, "two")
	for _, name := range []string{"one", "two"} {
		appKeys, err := m.store.AppKeys(name)
		require.NoError(t, err)
		require.NoError(t, m.testMachine().SetKeys(name, appKeys, []string{testProfileKey}))
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
	assert.Contains(t, ops.skeletons[m.testMachine().AppFiles("blog").Path()], "hostit.yml")
	assert.Contains(t, ops.skeletons[m.testMachine().AppFiles("blog").Path()], "README.md")
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
	for _, name := range []string{"", "Foo", "-x", "x-", "a_b", "a.b", "root", "hostit", "api",
		"www-data", "mariadb", "node", "control", "hostit-control", "grafana", "nginx", strings.Repeat("x", 33)} {
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

// A name taken by a unix account that hostit does not know about is refused --
// by the NODE, which is the only party that can see its own passwd file.
// Control used to check this itself, which only ever worked because it shared
// the machine's host.
func TestCreateAppExistingUnixUser(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	ops.existingUsers = []string{"phil"}
	err := m.testMachine().Provision(&ProvisionSpec{Name: "phil", ID: "id-phil", Port: 10000})
	require.ErrorIs(t, err, ErrAppExists)
}

// Control refuses a name that collides with a real account on ITS OWN host (a
// system/service/human user), so relay stubs and single-box app users cannot
// shadow or be shadowed by one. This is control's half of "node AND control do
// not already have the user"; the node checks its own passwd separately.
func TestCreateAppRejectsControlHostAccount(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	// "deploy" is not in the static reserved list, so only the passwd check can
	// catch it -- exactly the path under test.
	m.lookupOSUser = func(name string) (string, bool) {
		if name == "deploy" {
			return "/home/deploy", true // a real human/service account, not hostit-managed
		}
		return "", false
	}
	_, err := m.CreateApp("deploy", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.ErrorIs(t, err, ErrInvalid)
}

// A just-deleted app's unix user lingers under the apps pool while it tears
// down; control must treat that home as managed and NOT block re-creating the
// name (the node waits the teardown out).
func TestCreateAppAllowsRecreateOfManagedAccount(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	m.lookupOSUser = func(name string) (string, bool) {
		return filepath.Join(m.config.AppsDir, "id-x", "home", "app"), true
	}
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
}

// On a colocated node the app user is homed under the NODE's apps pool, which is
// NOT control's config.AppsDir. A just-deleted app tearing down there must not be
// mistaken for a foreign account, or an immediate same-name recreate 400s.
func TestCreateAppAllowsRecreateOfColocatedNodeUser(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	// An app user's home is the runtime raw-view path, under /run/hostit.
	m.lookupOSUser = func(name string) (string, bool) {
		return "/run/hostit/node/apps-raw/3c1117dca28d/home/app", true
	}
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
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

// The port range is fixed (workspace.PortMin/PortMax), so exhaustion is
// reached by reserving the whole span rather than by shrinking the range:
// allocatePort must report it instead of handing out a port twice.
func TestAllocatePortReportsAnExhaustedRange(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	m.pmu.Lock()
	for port := workspace.PortMin; port <= workspace.PortMax; port++ {
		m.reservedPorts[port] = true
	}
	m.pmu.Unlock()

	_, err := m.allocatePort()
	require.ErrorIs(t, err, ErrNoPortsAvailable)
	_, err = m.CreateApp("one", &CreateOptions{RequestKeys: []string{testPublicKey}})
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
	m.WaitBackground() // the user teardown runs in the background
	assert.Equal(t, []string{"blog"}, ops.deletedUsers)
	_, err = m.App("blog")
	require.ErrorIs(t, err, store.ErrAppNotFound)
	require.ErrorIs(t, m.DeleteApp("blog"), store.ErrAppNotFound)
}

// A deleted app must not leave its subvolume (or the root-owned stub userdel
// can leave where it was) behind under AppsDir.
func TestDeleteAppRemovesAppDirectory(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.testMachine().WriteFile("blog", "index.html", []byte("hi"), 0))
	require.DirExists(t, m.testMachine().AppSubvolume("blog"))

	require.NoError(t, m.DeleteApp("blog"))
	assert.NoDirExists(t, m.testMachine().AppSubvolume("blog"))
}

func TestSetKeys(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	otherKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL other@host"
	require.NoError(t, m.testMachine().SetKeys("blog", []string{otherKey}, nil))
	assert.Equal(t, []string{otherKey}, ops.authorizedKeys["blog"])
	require.Error(t, m.testMachine().SetKeys("blog", []string{"garbage"}, nil))
	require.ErrorIs(t, m.testMachine().SetKeys("nope", []string{otherKey}, nil), store.ErrAppNotFound)
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

// newTestManagerDeps builds the config, store and fake system services a test
// Manager needs, so callers can construct the Manager via NewManager with their own
// runner (keeping every runner-backed service -- btrfs, etc. -- on that one runner).
func newTestManagerDeps(t *testing.T) (*config.Config, *store.Store, *fakeSystem) {
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
	return conf, s, newFakeSystem()
}

func newTestManager(t *testing.T) (*Manager, *fakeSystem) {
	t.Helper()
	conf, s, ops := newTestManagerDeps(t)
	m := newWiredManager(t, conf, s, testServices(ops, newFakeRunner()))
	// Cleanups run LIFO: registered after the store-close cleanup, this waits
	// out the manager's background goroutines (post-create starts, delete
	// teardowns) BEFORE the db closes and the temp dirs vanish under them --
	// the source of the "database is closed" noise and a flaky TempDir
	// "directory not empty" failure in CI.
	t.Cleanup(m.WaitBackground)
	return m, ops
}

// testServices bundles the fake privileged services (users, ssh keys, firewall)
// with the runner-backed node-local services onto one node.Services, so a test
// records system calls instead of executing them.
func testServices(ops *fakeSystem, runner run.Runner) *node.Services {
	return &node.Services{
		Btrfs:     btrfs.New(runner),
		Systemd:   systemd.New(runner),
		Container: podman.New(runner),
		User:      ops,
		SSH:       ops,
		Firewall:  ops,
		Runner:    runner,
	}
}

// fakeSystem records the privileged system calls (users, ssh keys, firewall)
// instead of executing them; it satisfies the unixuser, ssh and firewall interfaces.
type fakeSystem struct {
	existingUsers  []string
	accounts       []unixuser.Account
	createdUsers   []string
	deletedUsers   []string
	renamedUsers   []string
	killedUsers    []string
	authorizedKeys map[string][]string
	skeletons      map[string][]string
	uids           map[string]int
	portRules      [][]nftables.Rule
	createUserErr  error

	mu sync.Mutex // Protects everything above: CreateApp starts the app in the background
}

var (
	_ unixuser.Interface = (*fakeSystem)(nil)
	_ ssh.Interface      = (*fakeSystem)(nil)
	_ nftables.Interface = (*fakeSystem)(nil)
)

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		authorizedKeys: make(map[string][]string),
		skeletons:      make(map[string][]string),
		uids:           make(map[string]int),
	}
}

func (f *fakeSystem) LookupIDs(username string) (uid, gid int, err error) {
	u, _ := f.LookupUID(username)
	return u, u, nil
}

// accounts is what the host would report for the app group; the reconcile
// sweep filters it by home, so tests set homes to place them in (or outside)
// this node's pool.
func (f *fakeSystem) List() ([]unixuser.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts, nil
}

func (f *fakeSystem) Exists(username string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range append(f.existingUsers, f.createdUsers...) {
		if u == username {
			return true
		}
	}
	return false
}

func (f *fakeSystem) LookupUID(username string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if uid, ok := f.uids[username]; ok {
		return uid, nil
	}
	return 1001, nil
}

func (f *fakeSystem) CreateStub(username, home string) error { return f.Create(username, home, 0) }

func (f *fakeSystem) Create(username, home string, uid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createUserErr != nil {
		return f.createUserErr
	}
	f.createdUsers = append(f.createdUsers, username)
	f.uids[username] = uid
	return nil
}

func (f *fakeSystem) KillProcesses(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killedUsers = append(f.killedUsers, username)
	return nil
}

// Rename moves all the fake's per-user state from the old login name to the
// new one, so lookups keep working after a rename just as the real usermod -l does.
func (f *fakeSystem) SetHome(string, string) error { return nil }
func (f *fakeSystem) Rename(oldName, newName string) error {
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

// Delete records the deletion AND removes the account, like the real userdel:
// Exists must flip to false, or a delete-then-recreate would wait forever.
func (f *fakeSystem) Delete(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedUsers = append(f.deletedUsers, username)
	f.existingUsers = slices.DeleteFunc(f.existingUsers, func(u string) bool { return u == username })
	f.createdUsers = slices.DeleteFunc(f.createdUsers, func(u string) bool { return u == username })
	return nil
}

// WriteAuthorizedKeys records the keys; the root comes from the Manager's
// chained files-root open, which the fake does not need to touch.
func (f *fakeSystem) WriteAuthorizedKeys(root *os.Root, username string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizedKeys[username] = keys
	return nil
}

// WriteSkeleton records the skeleton AND writes it, so tests that read app
// files (README, hostit.yml) see what a real app would have
// WriteSkeleton records the skeleton BY HOME PATH (it no longer learns the app
// name) AND writes it, so tests that read app files see what a real app would.
func (f *fakeSystem) WriteSkeleton(home string, files map[string]string) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		f.skeletons[home] = append(f.skeletons[home], name)
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

func (f *fakeSystem) Apply(rules []nftables.Rule) error {
	f.portRules = append(f.portRules, rules)
	return nil
}

func TestAllocatePortReservesUntilRegistered(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	// Two creates can run concurrently (the UI and an agent, or two agents), and
	// the port only shows up in the store's UsedPorts at AddApp time -- seconds
	// after allocation now that rootfs creation sits in between. Allocation must
	// therefore reserve the port in memory until it is registered or released:
	// handing both creates port N made the second useradd fail on a taken uid
	// (seen live: a UI create 500'd against a concurrent e2e create).
	first, err := m.allocatePort()
	require.NoError(t, err)
	second, err := m.allocatePort()
	require.NoError(t, err)
	assert.NotEqual(t, first, second, "an unregistered port must not be handed out twice")
	// Released ports become allocatable again (the failed-create path) -- but
	// not immediately: allocation rotates past them (their uid's budget qgroup
	// may still hold uncommitted bytes), so the released port comes back only
	// after the range wraps around.
	m.releasePort(second)
	third, err := m.allocatePort()
	require.NoError(t, err)
	assert.NotEqual(t, first, third, "a reserved port must never be handed out")
	assert.NotEqual(t, second, third, "a just-released port is skipped, not reused immediately")
}

func TestDeleteAppAnswersBeforeTeardownAndConverges(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	subvol := m.testMachine().AppSubvolume("blog")
	unit, container := m.testMachine().UnitName("blog"), m.testMachine().ContainerName("blog")
	group := fmt.Sprintf("1/%d", workspace.UIDFor(a.Port))
	r.reset()

	// The API answer is immediate: the rows are gone before any host teardown
	// (container stop, subvolume deletes, the qgroup sync ladder, userdel) --
	// the slow parts continue in the background.
	require.NoError(t, m.DeleteApp("blog"))
	_, err := m.App("blog")
	require.ErrorIs(t, err, store.ErrAppNotFound)

	// The background teardown converges to exactly what the sync delete did.
	// Both halves: control answers the delete, the node destroys the subvolume
	// and the budget qgroup in its own background.
	m.WaitBackground()
	m.testMachine().WaitBackground()
	joined := r.ran()
	assert.Contains(t, joined, "systemctl disable --now "+unit)
	assert.Contains(t, joined, "podman rm --force "+container)
	assert.Contains(t, joined, "btrfs subvolume delete "+subvol)
	assert.Contains(t, joined, "btrfs qgroup destroy "+group+" "+m.config.AppsDir)
	assert.Contains(t, ops.deletedUsers, "blog")
	assert.NoDirExists(t, subvol)

	// The port (and with it the uid block) stays reserved until the teardown is
	// done -- a new app grabbing it mid-userdel would collide -- and is free after.
	m.pmu.Lock()
	reserved := m.reservedPorts[a.Port]
	m.pmu.Unlock()
	assert.False(t, reserved, "the port is released once the teardown finished")
}

func TestDeleteThenRecreateSameNameWaitsForTheTeardown(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.DeleteApp("blog"))
	// The unix user dies in the background; an immediate same-name create must
	// wait that out rather than fail with "already exists".
	a, err := m.CreateApp("blog", &CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "blog", a.Name)
}

// A new app starts with the default CPU cap stamped as its own override --
// CPU has no owner-inheritable default, so the stamp at create IS the default,
// and an admin can raise or clear it per app afterwards.
func TestCreateStampsTheDefaultCPUCap(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("blog", &CreateOptions{})
	require.NoError(t, err)
	a, err := m.Store().App("blog")
	require.NoError(t, err)
	assert.Equal(t, defaultCPUMilli, a.CPUMilli)
	assert.Equal(t, defaultCPUMilli, m.CPULimit("blog"), "recorded for the desired state too")
}
