package node

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realExitError returns a genuine *exec.ExitError carrying the given status, the
// way a failed "podman exec" would -- there is no other way to fabricate one.
func realExitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	require.Error(t, err)
	return err
}

func TestExecTimeoutIsBounded(t *testing.T) {
	t.Parallel()
	// The timeout is bounded whatever the caller asks for: this runs on the
	// daemon's request path, on a box with one core
	assert.Equal(t, ExecDefaultTimeout, execTimeout(0))
	assert.Equal(t, 30*time.Second, execTimeout(30*time.Second))
	assert.Equal(t, ExecMaxTimeout, execTimeout(time.Hour))
}

func TestExecCapsItsOutput(t *testing.T) {
	t.Parallel()
	// A build that prints megabytes must not become megabytes of JSON in a
	// response, or megabytes held in the daemon
	long := strings.Repeat("x", ExecMaxOutput+5000)
	capped, truncated := capOutput(long)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(capped), ExecMaxOutput+200)
	assert.Contains(t, capped, "truncated")
	// The tail is what a build error lives in, so that is the end kept
	assert.True(t, strings.HasSuffix(capped, strings.Repeat("x", 100)))

	short, truncated := capOutput("all good")
	assert.False(t, truncated)
	assert.Equal(t, "all good", short)
}

func TestExitCodeHelper(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, exitCode(nil))
	assert.Equal(t, 7, exitCode(realExitError(t, 7)))
	// A failure that is not an ExitError (podman itself missing, context expiry)
	// never ran the command: report it as -1, not as a clean exit.
	assert.Equal(t, -1, exitCode(assert.AnError))
}
