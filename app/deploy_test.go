package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	runner.errs["image exists"] = assert.AnError      // Workspace image missing -> build
	runner.errs["container inspect"] = assert.AnError // No container yet -> create
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "deployed")
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "blog: podman build --tag "+workspaceImage)
	assert.Contains(t, joined, "blog: podman create --name hostit-app")
	assert.Contains(t, joined, "blog: systemctl --user daemon-reload")
	assert.Contains(t, joined, "blog: systemctl --user enable hostit-app")
	assert.Contains(t, joined, "blog: systemctl --user restart hostit-app")
	// Unit file written into the user's systemd dir
	unit, ok := ops.userFiles["blog:.config/systemd/user/hostit-app.service"]
	require.True(t, ok)
	assert.Contains(t, unit, "start --attach hostit-app")
}

func TestUpWorkspaceModeUnchangedOnlyReloadsAgent(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	// Existing container reports the exact hash of the desired config
	conf := mustLoadConfig(t, m, "blog")
	hash := containerConfigHash(containerCreateArgs(conf, a, m.appHome("blog"), m.config.SocketFile, hostitBinFile, 0))
	runner.outputs["container inspect"] = hash
	runner.outputs["is-active"] = "active"
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "reloaded")
	joined := strings.Join(runner.commands, "\n")
	assert.NotContains(t, joined, "podman create")
	assert.NotContains(t, joined, "podman rm")
	assert.Contains(t, joined, "podman kill --signal HUP hostit-app")
}

func TestUpImageModeRecreatesOnChange(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "image: docker.io/library/nginx:alpine\ncontainer-port: 80")
	runner.outputs["container inspect"] = "oldhash"
	msg, err := m.Up("blog")
	require.NoError(t, err)
	assert.Contains(t, msg, "deployed")
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "podman rm --force hostit-app")
	assert.Contains(t, joined, "podman create")
	assert.Contains(t, joined, "docker.io/library/nginx:alpine")
	assert.NotContains(t, joined, "podman build") // No build:, no workspace image needed
}

func TestUpBuildModeBuildsImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "build: .\ncontainer-port: 8080")
	runner.errs["container inspect"] = assert.AnError
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "podman build --tag "+buildImageTag+" "+m.appHome("blog"))
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
	runner.errs["image exists"] = assert.AnError
	runner.errs["container inspect"] = assert.AnError
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "podman build --tag "+workspaceImage)
	assert.Contains(t, joined, "podman create --name hostit-app")
	assert.Contains(t, joined, workspaceImage)
	assert.Contains(t, joined, "systemctl --user restart hostit-app")
}

func TestEnsureRunningContainerIsNoOp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.outputs["container inspect"] = "whatever" // Exists (hash mismatch MUST NOT recreate on ensure)
	runner.outputs["is-active"] = "active"
	_, err := m.Ensure("blog")
	require.NoError(t, err)
	joined := strings.Join(runner.commands, "\n")
	assert.NotContains(t, joined, "podman create")
	assert.NotContains(t, joined, "podman rm")
	assert.NotContains(t, joined, "restart")
}

func TestDeleteAppStopsAppBeforeRemovingUser(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.DeleteApp("blog"))
	joined := strings.Join(runner.commands, "\n")
	// A running container keeps processes alive, which makes userdel fail
	assert.Contains(t, joined, "systemctl --user disable --now hostit-app")
	assert.Contains(t, joined, "podman rm --force hostit-app")
	assert.Equal(t, []string{"blog"}, ops.deletedUsers)
}

func TestDownRestartStatus(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.Down("blog"))
	require.NoError(t, m.Restart("blog"))
	runner.outputs["status"] = "some status output"
	out, err := m.Status("blog")
	require.NoError(t, err)
	assert.Contains(t, out, "some status output")
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "systemctl --user disable --now hostit-app")
	assert.Contains(t, joined, "systemctl --user restart hostit-app")
}

func TestLogsWorkspaceModeReadsFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	require.NoError(t, os.MkdirAll(filepath.Join(m.appHome("blog"), ".hostit"), 0755))
	writeAppFile(t, m, "blog", ".hostit/app.log", "line1\nline2\nline3\n")
	out, err := m.Logs("blog", 2)
	require.NoError(t, err)
	assert.Equal(t, "line2\nline3\n", out)
}

func TestLogsImageModeUsesPodman(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "image: nginx\ncontainer-port: 80")
	runner.outputs["podman logs"] = "container logs here"
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

func newTestDeployManager(t *testing.T) (*Manager, *fakeSystemOps, *fakeUserRunner) {
	t.Helper()
	m, ops := newTestManager(t)
	runner := newFakeUserRunner()
	m.runner = runner
	return m, ops, runner
}

func createTestApp(t *testing.T, m *Manager, name string) *store.App {
	t.Helper()
	a, _, err := m.CreateApp(name, &CreateOptions{RequestKeys: []string{testPublicKey}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(m.appHome(name), 0755))
	return a
}

func writeAppFile(t *testing.T, m *Manager, name, filename, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(m.appHome(name), filename), []byte(content), 0600))
}

func mustLoadConfig(t *testing.T, m *Manager, name string) *appctl.AppConfig {
	t.Helper()
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
	require.NoError(t, err)
	return conf
}

// fakeUserRunner records as-user commands and returns canned outputs/errors
// matched by substring
type fakeUserRunner struct {
	commands []string
	outputs  map[string]string
	errs     map[string]error
}

var _ UserRunner = (*fakeUserRunner)(nil)

func newFakeUserRunner() *fakeUserRunner {
	return &fakeUserRunner{
		outputs: make(map[string]string),
		errs:    make(map[string]error),
	}
}

func (f *fakeUserRunner) RunAsUser(username string, args ...string) (string, error) {
	cmd := username + ": " + strings.Join(args, " ")
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
