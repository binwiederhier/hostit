package app

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/node"
)

// realExitError returns a genuine *exec.ExitError carrying the given status, the
// way a failed "podman exec" would -- there is no other way to fabricate one.
func realExitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	require.Error(t, err)
	return err
}

func TestExecPassesTheTimeoutIntoTheContainer(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")

	runner.reset()
	_, err := m.Exec("blog", "echo hi", 0)
	require.NoError(t, err)
	// The in-container timeout(1) is what actually bounds the command, so its
	// value must be the default in seconds, not 0 (which would kill instantly).
	assert.Contains(t, runner.ran(), "timeout --kill-after 5s 60")
	// The daemon's own bound is looser than the command's, so podman hanging is
	// caught but the command gets its full time first.
	require.NotEmpty(t, runner.timeouts)
	assert.Equal(t, node.ExecDefaultTimeout+node.ExecGraceTimeout, runner.timeouts[len(runner.timeouts)-1])

	runner.reset()
	_, err = m.Exec("blog", "echo hi", 10*time.Minute)
	require.NoError(t, err)
	// A request past the ceiling is clamped, not honored.
	assert.Contains(t, runner.ran(), "timeout --kill-after 5s 300")
	assert.Equal(t, node.ExecMaxTimeout+node.ExecGraceTimeout, runner.timeouts[len(runner.timeouts)-1])
}

func TestExecReportsATimeout(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.failOn("podman exec", realExitError(t, node.ExecTimeoutExitCode))

	res, err := m.Exec("blog", "sleep 999", 0)
	require.NoError(t, err) // A timed-out command is a result, not an error
	assert.True(t, res.TimedOut)
	assert.Equal(t, node.ExecTimeoutExitCode, res.ExitCode)
}

func TestExecReportsANonZeroExit(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	runner.failOn("podman exec", realExitError(t, 2))

	res, err := m.Exec("blog", "false", 0)
	require.NoError(t, err)
	assert.False(t, res.TimedOut)
	assert.Equal(t, 2, res.ExitCode)
}

func TestExecRefusesAPoweredOffApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// /run must behave like a login: a deliberately powered-off app is refused
	// with ErrPoweredOff (the API's 409), not handed to podman to fail with
	// "container state improper" buried in a 200 response.
	require.NoError(t, m.store.SetAppPoweredOff("blog", true))
	runner.reset()
	_, err := m.Exec("blog", "echo hi", 0)
	require.ErrorIs(t, err, appctl.ErrPoweredOff)
	assert.NotContains(t, runner.ran(), "podman exec")
}

func TestExecStartsAStoppedContainerFirst(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// An enabled app whose container is not running (a fresh fork, a crash, a
	// reboot) is brought up the same way a login would, then exec'd into --
	// otherwise /run races the background start and podman errors out.
	runner.returns("is-enabled", "enabled")
	runner.returns("container inspect", "whatever") // Exists
	runner.returns("is-active", "inactive")
	runner.reset()
	_, err := m.Exec("blog", "echo hi", 0)
	require.NoError(t, err)
	joined := runner.ran()
	assert.Contains(t, joined, "enable --now", "the container is started before the exec")
	assert.Contains(t, joined, "timeout --kill-after", "the command still runs")
}
