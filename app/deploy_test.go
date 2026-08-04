package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

func TestUpWorkspaceModeCreatesContainer(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: python3 -m http.server $PORT")
	runner.failOn("container inspect", assert.AnError) // No container yet -> create
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "deployed")
	// The workspace image is built once, host-wide, not per app
	require.Len(t, ops.builds, 1)
	assert.Equal(t, workspaceImageTag(), ops.builds[0].tag)
	joined := runner.ran()
	assert.Contains(t, joined, "podman create --name hostit-app-blog")
	assert.Contains(t, joined, "systemctl enable --now hostit-app@blog")
	assert.Contains(t, joined, "systemctl restart hostit-app@blog")
}

func TestUpWorkspaceModeUnchangedOnlyReloadsAgent(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
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
	assert.Contains(t, joined, "podman kill --signal HUP hostit-app-blog")
}

func TestUpImageModeRecreatesOnChange(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "image: docker.io/library/nginx:alpine\ncontainer-port: 80")
	runner.returns("container inspect", "oldhash")
	runner.reset()
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "deployed")
	joined := runner.ran()
	assert.Contains(t, joined, "podman rm --force hostit-app-blog")
	assert.Contains(t, joined, "podman create")
	assert.Contains(t, joined, "docker.io/library/nginx:alpine")
	assert.NotContains(t, joined, "podman build") // No build:, no workspace image needed
}

func TestUpBuildModeBuildsImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "build: .\ncontainer-port: 8080")
	runner.failOn("container inspect", assert.AnError)
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.Contains(t, joined, "podman build --tag "+buildImageTag("blog")+" "+m.appHome("blog"))
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

func TestEnsureWithoutConfigCreatesIdleWorkspace(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.failOn("container inspect", assert.AnError)
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.Contains(t, joined, "podman create --name hostit-app-blog")
	assert.Contains(t, joined, workspaceImageTag())
	assert.Contains(t, joined, "systemctl restart hostit-app@blog")
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
	runner.reset()
	require.NoError(t, m.DeleteApp("blog"))
	joined := runner.ran()
	// A running container keeps processes alive, which makes userdel fail
	assert.Contains(t, joined, "systemctl disable --now hostit-app@blog")
	assert.Contains(t, joined, "podman rm --force hostit-app-blog")
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
	assert.Contains(t, joined, "systemctl disable --now hostit-app@blog")
	assert.Contains(t, joined, "systemctl restart hostit-app@blog")
}

func TestLogsWorkspaceModeReadsFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	writeAppFile(t, m, "blog", appctl.LogDir+"/app.log", "line1\nline2\nline3\n")
	out, err := m.Logs("blog", 2)
	require.NoError(t, err)
	assert.Equal(t, "line2\nline3\n", out)
}

func TestLogsImageModeUsesPodman(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "image: nginx\ncontainer-port: 80")
	runner.returns("podman logs", "container logs here")
	out, err := m.Logs("blog", 50)
	require.NoError(t, err)
	assert.Contains(t, out, "container logs here")
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
	assert.Equal(t, "blog", "blog") // Remaining rule belongs to blog
	assert.Equal(t, 10000, last[0].Port)
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
	m, ops := newTestManager(t)
	runner := newFakeRunner()
	m.runner = runner
	return m, ops, runner
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
	require.Eventually(t, func() bool {
		return strings.Contains(runner.ran(), "hostit-app@"+name)
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
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
	require.NoError(t, err)
	return conf
}

// fakeRunner records root commands and returns canned outputs/errors matched by
// substring
type fakeRunner struct {
	commands []string
	outputs  map[string]string
	errs     map[string]error
	mu       sync.Mutex // Protects commands; the demo app deploys in the background
}

var _ Runner = (*fakeRunner)(nil)

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		outputs: make(map[string]string),
		errs:    make(map[string]error),
	}
}

func (f *fakeRunner) RunTimeout(timeout time.Duration, args ...string) (string, error) {
	return f.Run(args...)
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
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

func TestMountSourcesMustResolveInsideTheApp(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	home := m.appHome("blog")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "data"), 0o755))
	// A relative source passes validation, so the link is the way out
	require.NoError(t, os.Symlink("/etc", filepath.Join(home, "escape")))

	conf := &appctl.AppConfig{Image: "nginx", ContainerPort: 80, Volumes: []string{"data:/data"}}
	require.NoError(t, m.checkMountSources("blog", conf), "a real directory in the app is fine")

	conf.Volumes = []string{"escape:/hostetc"}
	require.ErrorIs(t, m.checkMountSources("blog", conf), ErrInvalid, "a link out of the app must be refused")

	conf.Volumes = nil
	conf.Image = ""
	conf.Build = "escape"
	require.ErrorIs(t, m.checkMountSources("blog", conf), ErrInvalid, "so must a build context")
}

func TestRestartStaleAgents(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "static: public")

	// A running agent is the binary it was exec'd from: after an upgrade it
	// keeps the old behaviour until its container restarts. That is how a
	// changed static: directory once kept serving the old one.
	restarted, err := m.RestartStaleAgents("v0.3.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"blog"}, restarted)
	assert.Contains(t, runner.ran(), "systemctl restart hostit-app@blog")

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
	writeAppFile(t, m, "blog", "hostit.yml", "static: public")

	// The command runs inside the app's own container, through a shell so that
	// "cd src && go build" works, and never on the host
	res, err := m.Exec("blog", "go version", 0)
	require.NoError(t, err)
	assert.Contains(t, runner.ran(), "podman exec")
	assert.Contains(t, runner.ran(), containerName("blog"))
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
