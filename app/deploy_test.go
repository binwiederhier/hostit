package app

import (
	"errors"
	"os"
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
	assert.Contains(t, runner.ran(), "podman build --tag "+workspaceImageTag())
	joined := runner.ran()
	assert.Contains(t, joined, "podman create --name "+m.containerName("blog"))
	assert.Contains(t, joined, "systemctl enable --now "+m.unitName("blog"))
	assert.Contains(t, joined, "systemctl restart "+m.unitName("blog"))
}

func TestUpWorkspaceModeUnchangedOnlyReloadsAgent(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	// Existing container reports the exact hash of the desired config
	conf := mustLoadConfig(t, m, "blog")
	ids, err := ops.LookupIDs("blog")
	require.NoError(t, err)
	hash := containerConfigHash(containerCreateArgs(conf, a, m.appHome("blog"), m.config.SocketFile, hostitBinFile, 0, ids))
	runner.returns("container inspect", hash)
	runner.returns("is-active", "active")
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
	link := filepath.Join(m.appHome("blog"), "hostit.yml")
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
	assert.Contains(t, joined, workspaceImageTag())
	assert.Contains(t, joined, "systemctl restart "+m.unitName("blog"))
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
	ids, err := ops.LookupIDs("blog")
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

func newTestDeployManager(t *testing.T) (*Manager, *fakeSystemOps, *fakeRunner) {
	t.Helper()
	conf, s, ops := newTestManagerDeps(t)
	runner := newFakeRunner()
	return NewManager(conf, s, ops, runner), ops, runner
}

// createTestApp creates an app and waits for the background demo deploy to
// finish, so tests assert on their own actions rather than racing that goroutine
func createTestApp(t *testing.T, m *Manager, name string) *store.App {
	t.Helper()
	a, err := m.CreateApp(name, &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(m.appHome(name), 0755))
	runner, ok := m.runner.(*fakeRunner)
	if !ok {
		return a
	}
	// Wait for the deploy's last command (the agent restart), not just the first
	// time the unit is mentioned: otherwise the goroutine's trailing commands leak
	// past a reset() the test does next.
	require.Eventually(t, func() bool {
		return strings.Contains(runner.ran(), "restart "+m.unitName(name))
	}, 5*time.Second, 5*time.Millisecond, "background demo deploy did not settle")
	return a
}

func writeAppFile(t *testing.T, m *Manager, name, filename, content string) {
	t.Helper()
	full := filepath.Join(m.appHome(name), filename)
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
	mu       sync.Mutex      // Protects commands; the demo app deploys in the background
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
	home := m.appHome("blog")

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
	// changed static: directory once kept serving the old one.
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
