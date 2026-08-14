package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

func TestUpWorkspaceModeCreatesContainer(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: python3 -m http.server $PORT")
	runner.failOn("container inspect", assert.AnError) // No container yet -> create
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "deployed")
	// The workspace image is built once, host-wide, not per app
	assert.Contains(t, runner.ran(), "podman build --tag "+workspace.ImageTag())
	joined := runner.ran()
	assert.Contains(t, joined, "podman create --name "+m.containerName("blog"))
	assert.Contains(t, joined, "systemctl enable --now "+m.unitName("blog"))
	// Exactly ONE start: enable --now brings the fresh unit up attached to the new
	// container. The old enable-then-restart pair started every new app twice --
	// run 1 died ~300ms in when the restart tore it down, a churn window that
	// raced every early stop/start (seen live on stage; podman events showed
	// start/died/start on every create).
	assert.NotContains(t, joined, "systemctl restart "+m.unitName("blog"))
}

func TestUpWorkspaceModeUnchangedOnlyReloadsAgent(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	// Existing container reports the exact hash of the desired config
	conf := mustLoadConfig(t, m, "blog")
	ids, err := m.lookupIDs("blog")
	require.NoError(t, err)
	hash := workspace.ConfigHash(workspace.CreateArgs(conf, a, m.appSubvolume("blog"), m.config.SocketFile, hostitBinFile, Version, 0, ids))
	runner.returns("container inspect", hash)
	runner.returns("is-active", "active")
	// The app's subvolume already exists (steady state), so the deploy must not
	// touch the base or export anything -- only the reload path below runs.
	require.NoError(t, os.MkdirAll(m.appSubvolume("blog"), 0o700))
	runner.reset()
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "reloaded")
	joined := runner.ran()
	assert.NotContains(t, joined, "podman create")
	assert.NotContains(t, joined, "podman rm")
	assert.Contains(t, joined, "podman kill --signal HUP "+m.containerName("blog"))
}

func TestUpRejectsTheRemovedContainerMode(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// An app written against the old "image:" mode must be told, not silently
	// served as something else
	writeAppFile(t, m, "blog", "hostit.yml", "image: docker.io/library/nginx:alpine\ncontainer-port: 80")
	_, err := m.Up("blog")
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "image")
}

func TestUpInvalidConfig(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "# nothing configured")
	_, err := m.Up("blog")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostit.yml")
}

func TestUpRefusesASymlinkedConfig(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// A tenant owns their home, so they can replace hostit.yml with a symlink out
	// of it. The daemon reads that file as root: following the link would let a
	// tenant point it at /dev/zero (read forever, OOM the whole daemon) or a
	// root-only file. Reading through the app's os.Root must refuse the symlink,
	// whatever it points at, rather than dereferencing it.
	outside := filepath.Join(t.TempDir(), "outside.yml")
	require.NoError(t, os.WriteFile(outside, []byte("mode: static\n"), 0o600))
	link := filepath.Join(m.appFiles("blog").Path(), "hostit.yml")
	require.NoError(t, os.Remove(link)) // Drop the skeleton config, then plant the symlink
	require.NoError(t, os.Symlink(outside, link))
	_, err := m.Up("blog")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestEnsureWithoutConfigCreatesIdleWorkspace(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.failOn("container inspect", assert.AnError)
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.Contains(t, joined, "podman create --name "+m.containerName("blog"))
	assert.Contains(t, joined, workspace.ImageTag())
	assert.Contains(t, joined, "systemctl enable --now "+m.unitName("blog"))
	assert.NotContains(t, joined, "systemctl restart "+m.unitName("blog"))
}

func TestDeployReusesTheOneWorkspaceImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	runner.seedImage(workspace.ImageTag()) // Built once, host-wide
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	runner.failOn("container inspect", assert.AnError) // Force a create
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.NotContains(t, joined, "podman build", "the workspace image is shared, never rebuilt per app")
	assert.Contains(t, joined, "podman create --name "+m.containerName("blog"))
}

func TestContainerRunsUnderTheAppsOwnIdentity(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	runner.failOn("container inspect", assert.AnError)
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := runner.ran()
	// Container root maps to the app's unprivileged uid, so an escape lands
	// there rather than on real root, and its own network stack keeps it from
	// reaching other apps. One contiguous block, so podman idmap-mounts the image.
	uid := m.uidFor(10000)
	assert.Contains(t, joined, fmt.Sprintf("--uidmap 0:%d:65536", uid))
	assert.Contains(t, joined, fmt.Sprintf("--gidmap 0:%d:65536", uid))
	assert.Contains(t, joined, "--network slirp4netns")
	assert.Contains(t, joined, "--publish 127.0.0.1:10000:80")
}

func TestEnsureRunningContainerIsNoOp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.returns("container inspect", "whatever") // Exists (hash mismatch MUST NOT recreate on ensure)
	runner.returns("is-active", "active")
	runner.reset()
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.NotContains(t, joined, "podman create")
	assert.NotContains(t, joined, "podman rm")
	assert.NotContains(t, joined, "restart")
}

func TestEnsureRefusesAPoweredOffApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// A poweroff records intent in the store. A login (SSH or the web terminal)
	// must not power it back on -- otherwise poweroff never sticks, and an
	// auto-reconnecting terminal fights the operator.
	require.NoError(t, m.store.SetAppPoweredOff("blog", true))
	runner.reset()
	_, err := m.Ensure("blog")
	require.ErrorIs(t, err, appctl.ErrPoweredOff)
	joined := runner.ran()
	assert.NotContains(t, joined, "enable --now", "a powered-off app must not be re-enabled on login")
	assert.NotContains(t, joined, "restart")
	assert.NotContains(t, joined, "podman create")
}

func TestEnsureStartsAnEnabledButStoppedApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// Enabled but not running (e.g. crashed, or a fresh reboot): a login should
	// still bring it up -- only a deliberate poweroff (disabled) is left alone.
	runner.returns("is-enabled", "enabled")
	runner.returns("container inspect", "whatever") // Exists
	runner.returns("is-active", "inactive")
	runner.reset()
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	assert.Contains(t, runner.ran(), "enable --now", "an enabled-but-stopped app is started on login")
}

func TestPowerOnStartsAPoweredOffApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// PowerOn is the explicit verb: it must start (and re-enable) even a
	// deliberately powered-off app -- that is the whole point, unlike a login.
	runner.returns("is-enabled", "disabled")
	runner.returns("container inspect", "whatever") // Exists
	runner.returns("is-active", "inactive")
	runner.reset()
	_, err := m.PowerOn("blog")
	require.NoError(t, err)
	assert.Contains(t, runner.ran(), "enable --now", "power-on re-enables and starts the unit")
}

func TestDeleteAppStopsAppBeforeRemovingUser(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// Capture the id-keyed names before the delete removes the app (afterwards the
	// name no longer resolves to its id).
	unit, container := m.unitName("blog"), m.containerName("blog")
	runner.reset()
	require.NoError(t, m.DeleteApp("blog"))
	joined := runner.ran()
	// A running container keeps processes alive, which makes userdel fail
	assert.Contains(t, joined, "systemctl disable --now "+unit)
	assert.Contains(t, joined, "podman rm --force "+container)
	assert.Equal(t, []string{"blog"}, ops.deletedUsers)
}

func TestDownRestartStatus(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.reset()
	require.NoError(t, m.Down("blog"))
	require.NoError(t, m.Restart("blog"))
	runner.returns("status", "some status output")
	out, err := m.Status("blog")
	require.NoError(t, err)
	assert.Contains(t, out, "some status output")
	joined := runner.ran()
	assert.Contains(t, joined, "systemctl disable --now "+m.unitName("blog"))
	assert.Contains(t, joined, "systemctl restart "+m.unitName("blog"))
}

func TestLogsWorkspaceModeReadsFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	writeAppFile(t, m, "blog", appctl.LogDir+"/app.log", "line1\nline2\nline3\n")
	out, err := m.Logs("blog", 2)
	require.NoError(t, err)
	assert.Equal(t, "line2\nline3\n", out)
}

func TestPortRulesReconciledOnCreateAndDelete(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	createTestApp(t, m, "wiki")
	require.NotEmpty(t, ops.portRules)
	last := ops.portRules[len(ops.portRules)-1]
	require.Len(t, last, 2)
	assert.Equal(t, 10000, last[0].Port)
	assert.Equal(t, 10001, last[1].Port)
	assert.NotZero(t, last[0].UID)
	require.NoError(t, m.DeleteApp("wiki"))
	last = ops.portRules[len(ops.portRules)-1]
	require.Len(t, last, 1)
	// The one remaining rule is blog's: its port and its uid, not wiki's
	ids, err := m.lookupIDs("blog")
	require.NoError(t, err)
	assert.Equal(t, 10000, last[0].Port)
	assert.Equal(t, ids.UID, last[0].UID)
}

// failOn makes any command containing substr fail
func (f *fakeRunner) failOn(substr string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[substr] = err
}

// returns makes any command containing substr return out
func (f *fakeRunner) returns(substr, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outputs[substr] = out
}

// ran returns the commands recorded so far, joined for substring assertions
func (f *fakeRunner) ran() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.commands, "\n")
}

// reset forgets recorded commands, so a test can assert on one action alone
func (f *fakeRunner) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = nil
}

func newTestDeployManager(t *testing.T) (*Manager, *fakeSystem, *fakeRunner) {
	t.Helper()
	conf, s, ops := newTestManagerDeps(t)
	runner := newFakeRunner()
	return NewManager(conf, s, testServices(ops, runner)), ops, runner
}

// createTestApp creates an app and waits for the background demo deploy to
// finish, so tests assert on their own actions rather than racing that goroutine
func createTestApp(t *testing.T, m *Manager, name string) *store.App {
	t.Helper()
	a, err := m.CreateApp(name, &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(m.appFiles(name).Path(), 0o755))
	runner, ok := m.runner.(*fakeRunner)
	if !ok {
		return a
	}
	// Wait for the deploy's last command, not just the first time the unit is
	// mentioned: otherwise the goroutine's trailing commands leak past a reset()
	// the test does next. The deploy finishes with exactly one start: enable --now
	// for a stopped unit, or a restart when a test stubs the unit as still active.
	require.Eventually(t, func() bool {
		ran := runner.ran()
		return strings.Contains(ran, "enable --now "+m.unitName(name)) ||
			strings.Contains(ran, "systemctl restart "+m.unitName(name))
	}, 5*time.Second, 5*time.Millisecond, "background demo deploy did not settle")
	return a
}

func writeAppFile(t *testing.T, m *Manager, name, filename, content string) {
	t.Helper()
	full := filepath.Join(m.appFiles(name).Path(), filename)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0600))
}

func mustLoadConfig(t *testing.T, m *Manager, name string) *appctl.AppConfig {
	t.Helper()
	conf, err := m.loadConfig(name)
	require.NoError(t, err)
	return conf
}

// fakeRunner records root commands and returns canned outputs/errors matched by
// substring
type fakeRunner struct {
	commands []string
	timeouts []time.Duration // Outer bound passed to each RunTimeout call, in order
	outputs  map[string]string
	errs     map[string]error
	built    map[string]bool // Image tags the fake store "has" (built or seeded)
	// emulateSubvolDelete makes "btrfs subvolume delete" actually remove the
	// path, for the migration tests that need the real on-disk result. Off by
	// default: the reconcile tests rely on the fake delete NOT touching disk
	// (standing in for the real tool refusing a plain directory).
	emulateSubvolDelete bool
	mu                  sync.Mutex // Protects commands; the demo app deploys in the background
}

var _ run.Runner = (*fakeRunner)(nil)

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		outputs: make(map[string]string),
		errs:    make(map[string]error),
		built:   make(map[string]bool),
	}
}

// errImageMissing is what a fake "podman image exists" returns for an image that
// was never built or seeded, so container.ImageExists reports false.
var errImageMissing = errors.New("image not known to the fake store")

// seedImage marks an image as already present in the fake store.
func (f *fakeRunner) seedImage(tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built[tag] = true
}

func (f *fakeRunner) RunTimeout(timeout time.Duration, args ...string) (string, error) {
	f.mu.Lock()
	f.timeouts = append(f.timeouts, timeout)
	f.mu.Unlock()
	return f.Run(args...)
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	// Track the image store the way the real podman does: a build makes the tag
	// exist, and "image exists" fails for a tag never built or seeded.
	if len(args) >= 4 && args[0] == "podman" && args[1] == "build" && args[2] == "--tag" {
		f.built[args[3]] = true
	}
	// Stand in for btrfs's on-disk effect so the create/fork/snapshot paths yield a
	// real directory on the non-btrfs test host: creating a subvolume, or snapshotting
	// one, materializes the destination path (the last argument). Without this the
	// home a fresh app's create path relies on would never exist.
	if len(args) >= 4 && args[0] == "btrfs" && args[1] == "subvolume" &&
		(args[2] == "create" || args[2] == "snapshot") {
		_ = os.MkdirAll(args[len(args)-1], 0o755)
	}
	// The base export publishes via a rename (MoveSubvolume); emulate it so the
	// base actually appears at its final path and a second ensure is a no-op.
	// ONLY for .bases and .rootfs paths (the latter is the unification
	// migration's staged rootfs moving into place): the rollback swap also moves
	// subvolumes, but the fake snapshot above materializes empty dirs, so a
	// faithful mv there would swap a populated test subvolume for an empty one.
	if len(args) == 3 && args[0] == "mv" && (strings.Contains(args[1], "/.bases/") || strings.Contains(args[1], "/.rootfs/")) {
		_ = os.Rename(args[1], args[2])
	}
	// The unification migration reflink-copies the home into the staged rootfs;
	// emulate with a plain cp -a (the test fs is not CoW, and the copy's effect
	// is what the migration tests observe).
	if len(args) == 5 && args[0] == "cp" && args[1] == "-a" && args[2] == "--reflink=always" {
		_ = exec.Command("cp", "-a", args[3], args[4]).Run()
	}
	if f.emulateSubvolDelete && len(args) == 4 && args[0] == "btrfs" && args[1] == "subvolume" && args[2] == "delete" {
		_ = os.RemoveAll(args[3])
	}
	if len(args) == 4 && args[0] == "podman" && args[1] == "image" && args[2] == "exists" {
		if !f.built[args[3]] {
			return "", errImageMissing
		}
		return "", nil
	}
	for substr, err := range f.errs {
		if strings.Contains(cmd, substr) {
			return "", err
		}
	}
	for substr, out := range f.outputs {
		if strings.Contains(cmd, substr) {
			return out, nil
		}
	}
	return "", nil
}

func TestLogsCannotBeSymlinkedToAnythingElse(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	home := m.appFiles("blog").Path()

	secret := filepath.Join(t.TempDir(), "server.yml")
	require.NoError(t, os.WriteFile(secret, []byte("admin-token: hunter2\n"), 0o600))
	// The app user owns log/ inside their container, so they can point the log
	// at anything the daemon (root) can read
	require.NoError(t, os.MkdirAll(filepath.Join(home, appctl.LogDir), 0o755))
	require.NoError(t, os.Symlink(secret, filepath.Join(home, appctl.LogDir, "app.log")))

	out, err := m.Logs("blog", 100)
	assert.NotContains(t, out, "hunter2", "logs must never read through a symlink")
	assert.Error(t, err)
}

func TestRestartStaleAgents(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: static")

	// A running agent is the binary it was exec'd from: after an upgrade it
	// keeps the old behaviour until its container restarts. That is how a
	// changed static: directory once kept serving the old one. The unit is
	// active here (the realistic upgrade case: apps are up), so re-attaching to
	// the recreated container takes a restart, not an enable.
	runner.returns("is-active", "active")
	restarted, err := m.RestartStaleAgents("v0.3.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"blog"}, restarted)
	assert.Contains(t, runner.ran(), "systemctl restart "+m.unitName("blog"))

	// Same version again is a no-op: restarts interrupt apps, so they only
	// happen when the binary behind the agents actually changed
	runner.reset()
	restarted, err = m.RestartStaleAgents("v0.3.0")
	require.NoError(t, err)
	assert.Empty(t, restarted)
	assert.NotContains(t, runner.ran(), "systemctl restart")

	// A new version restarts them again
	restarted, err = m.RestartStaleAgents("v0.4.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"blog"}, restarted)
}

func TestRestartStaleAgentsLeavesPoweredOffAppsOff(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: static")

	// The app was deliberately powered off (recorded intent). An upgrade --
	// including the storage migration's config-hash change that makes every
	// container stale -- must not resurrect it: Up would recreate the container
	// and enable the unit.
	require.NoError(t, m.store.SetAppPoweredOff("blog", true))
	runner.reset()
	restarted, err := m.RestartStaleAgents("v0.3.0")
	require.NoError(t, err)
	assert.Empty(t, restarted)
	joined := runner.ran()
	assert.NotContains(t, joined, "enable --now", "a powered-off app must not be re-enabled by an upgrade")
	assert.NotContains(t, joined, "systemctl restart")
	assert.NotContains(t, joined, "podman create")
}

func TestExecInApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: static")

	// The command runs inside the app's own container, through a shell so that
	// "cd src && go build" works, and never on the host
	res, err := m.Exec("blog", "go version", 0)
	require.NoError(t, err)
	assert.Contains(t, runner.ran(), "podman exec")
	assert.Contains(t, runner.ran(), m.containerName("blog"))
	assert.Contains(t, runner.ran(), "go version")
	assert.Equal(t, 0, res.ExitCode)

	// The limit is enforced inside the container, not just on the podman client:
	// killing "podman exec" on the host leaves the command running in the
	// container, burning the app's memory and CPU with nobody watching
	assert.Contains(t, runner.ran(), "timeout")
	assert.Contains(t, runner.ran(), "--kill-after")

	// An empty command is a mistake, not a shell prompt
	_, err = m.Exec("blog", "   ", 0)
	require.ErrorIs(t, err, ErrInvalid)

	// The timeout is bounded whatever the caller asks for: this runs on the
	// daemon's request path, on a box with one core
	assert.Equal(t, execDefaultTimeout, execTimeout(0))
	assert.Equal(t, 30*time.Second, execTimeout(30*time.Second))
	assert.Equal(t, execMaxTimeout, execTimeout(time.Hour))
}

func TestExecCapsItsOutput(t *testing.T) {
	t.Parallel()
	// A build that prints megabytes must not become megabytes of JSON in a
	// response, or megabytes held in the daemon
	long := strings.Repeat("x", execMaxOutput+5000)
	capped, truncated := capOutput(long)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(capped), execMaxOutput+200)
	assert.Contains(t, capped, "truncated")
	// The tail is what a build error lives in, so that is the end kept
	assert.True(t, strings.HasSuffix(capped, strings.Repeat("x", 100)))

	short, truncated := capOutput("all good")
	assert.False(t, truncated)
	assert.Equal(t, "all good", short)
}

func TestReconcileUnitsRemovesUnitsOfDeletedApps(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.reset()
	// Units and containers are keyed by app id, so the live app's are named by its
	// id; "gone"/"e2e-old"/"ghost" are orphan ids with no app behind them.
	runner.returns("systemctl list-units", m.unitName("blog")+".service loaded active running\nhostit-app@gone.service loaded failed failed\nhostit-app@e2e-old.service loaded activating auto-restart\n")

	runner.returns("podman ps", m.containerName("blog")+"\nhostit-app-ghost\nsomething-else\n")

	// A unit whose app is gone is not harmless: Restart=always keeps systemd
	// retrying it forever, and its enable symlink brings it back after a reboot
	removed := m.ReconcileOrphans()
	assert.ElementsMatch(t, []string{"gone", "e2e-old", "ghost"}, removed)
	ran := runner.ran()
	assert.Contains(t, ran, "systemctl disable --now hostit-app@gone")
	assert.Contains(t, ran, "systemctl reset-failed hostit-app@gone")
	assert.Contains(t, ran, "systemctl disable --now hostit-app@e2e-old")
	assert.NotContains(t, ran, "disable --now "+m.unitName("blog"), "a live app must be left alone")

	// Containers outlive their app the same way: deleting an app races the
	// background start that follows creating one, and the loser is a container
	// nothing will ever start
	assert.Contains(t, ran, "podman rm --force hostit-app-ghost")
	assert.NotContains(t, ran, "podman rm --force "+m.containerName("blog"))
	assert.NotContains(t, ran, "something-else", "only hostit's own containers are ours to remove")
}

func TestUpRefusesAnAppThatWasDeletedMeanwhile(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: static")
	require.NoError(t, m.store.RemoveApp("blog"))
	runner.reset()

	// Creating an app starts it in the background; deleting it a second later
	// used to leave that start to finish and recreate the container
	_, err := m.Up("blog")
	require.Error(t, err)
	assert.NotContains(t, runner.ran(), "podman create", "a deleted app must not get a container")
}

func TestStartAppRefusesAPoweredOffApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// Starting the app process needs the container, and a deliberately
	// powered-off app must be refused with ErrPoweredOff (the API's 409, same
	// as every other container-needing call), not a generic invalid error.
	require.NoError(t, m.store.SetAppPoweredOff("blog", true))
	runner.reset()
	err := m.StartApp("blog")
	require.ErrorIs(t, err, appctl.ErrPoweredOff)
	assert.NotContains(t, runner.ran(), "podman kill", "no signal is sent to a powered-off app")
}

func TestEnsureStartsAFreshAppWhoseUnitWasNeverEnabled(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// THE fresh-app blip: a template instance that was never enabled reads
	// "disabled" from systemctl is-enabled, exactly like a deliberate poweroff.
	// Poweroff intent is recorded in the store now, so a brand-new app (flag
	// unset) must start on login -- even while its unit still reads "disabled".
	runner.returns("is-enabled", "disabled")
	runner.returns("container inspect", "whatever") // Exists
	runner.returns("is-active", "inactive")
	runner.reset()
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	assert.Contains(t, runner.ran(), "enable --now", "a never-enabled fresh app must start on login")
}

func TestDownRecordsPowerOffAndPowerOnClearsIt(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")

	require.NoError(t, m.Down("blog"))
	a, err := m.store.App("blog")
	require.NoError(t, err)
	assert.True(t, a.PoweredOff, "poweroff is recorded intent")
	assert.Contains(t, runner.ran(), "systemctl disable --now "+m.unitName("blog"))

	// Ensure (a login) refuses on the FLAG -- no is-enabled stub involved.
	_, err = m.Ensure("blog")
	require.ErrorIs(t, err, appctl.ErrPoweredOff)
	// StartApp and Exec refuse the same way.
	require.ErrorIs(t, m.StartApp("blog"), appctl.ErrPoweredOff)
	_, err = m.Exec("blog", "echo hi", 0)
	require.ErrorIs(t, err, appctl.ErrPoweredOff)

	// PowerOn clears the intent and starts the app.
	runner.returns("container inspect", "whatever")
	runner.returns("is-active", "inactive")
	_, err = m.PowerOn("blog")
	require.NoError(t, err)
	a, err = m.store.App("blog")
	require.NoError(t, err)
	assert.False(t, a.PoweredOff)
}

func TestBackfillPowerOffFlags(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	createTestApp(t, m, "wiki")
	// Hosts upgrading to the flag: a disabled unit at backfill time IS a
	// deliberate poweroff (only poweroff disabled units before the flag existed).
	runner.returns("is-enabled hostit-app@"+m.appID("blog"), "disabled")
	runner.returns("is-enabled hostit-app@"+m.appID("wiki"), "enabled")
	r2 := runner // silence unused in case
	_ = r2

	require.NoError(t, m.BackfillPowerOffFlags())
	blog, err := m.store.App("blog")
	require.NoError(t, err)
	assert.True(t, blog.PoweredOff)
	wiki, err := m.store.App("wiki")
	require.NoError(t, err)
	assert.False(t, wiki.PoweredOff)

	// One-time: a later run must not resurrect the flag from unit state (the
	// flag is authoritative after the backfill).
	require.NoError(t, m.store.SetAppPoweredOff("blog", false))
	require.NoError(t, m.BackfillPowerOffFlags())
	blog, err = m.store.App("blog")
	require.NoError(t, err)
	assert.False(t, blog.PoweredOff, "the settings gate makes the backfill one-time")
}
