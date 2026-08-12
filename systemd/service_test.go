package systemd

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner records commands and can be primed to return canned output for a
// command whose joined args start with a given prefix.
type fakeRunner struct {
	ran     []string
	timed   []string
	outputs map[string]string
}

func newFakeRunner() *fakeRunner { return &fakeRunner{outputs: map[string]string{}} }

func (f *fakeRunner) returns(prefix, out string) { f.outputs[prefix] = out }

func (f *fakeRunner) output(joined string) string {
	for prefix, out := range f.outputs {
		if strings.HasPrefix(joined, prefix) {
			return out
		}
	}
	return ""
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, joined)
	return f.output(joined), nil
}

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	f.timed = append(f.timed, joined)
	return f.output(joined), nil
}

func TestVerbsIssueTheRightCommands(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	require.NoError(t, s.EnableNow("hostit-app@abc"))
	require.NoError(t, s.DisableNow("hostit-app@abc"))
	require.NoError(t, s.Start("hostit-app@abc"))
	require.NoError(t, s.Stop("hostit-app@abc"))
	require.NoError(t, s.Restart("hostit-app@abc"))
	require.NoError(t, s.ResetFailed("hostit-app@abc"))

	assert.Equal(t, []string{
		"systemctl enable --now hostit-app@abc",
		"systemctl disable --now hostit-app@abc",
		"systemctl start hostit-app@abc",
		"systemctl stop hostit-app@abc",
		"systemctl restart hostit-app@abc",
		"systemctl reset-failed hostit-app@abc",
	}, r.ran)
}

func TestStatusAndListUnits(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	_, err := s.Status("hostit-app@abc")
	require.NoError(t, err)
	assert.Contains(t, r.ran, "systemctl status --no-pager hostit-app@abc")

	_, err = s.ListUnits("hostit-app@*")
	require.NoError(t, err)
	assert.Contains(t, r.ran, "systemctl list-units hostit-app@* --all --no-legend --plain")
}

func TestIsActiveBatchesUnitsAndHonorsTimeout(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("systemctl is-active", "active\ninactive\n")
	s := New(r)

	// With a timeout, one call carries every unit in order.
	out, _ := s.IsActive(5*time.Second, "hostit-app@a", "hostit-app@b")
	assert.Equal(t, "active\ninactive\n", out)
	assert.Equal(t, []string{"systemctl is-active hostit-app@a hostit-app@b"}, r.timed)
	assert.Empty(t, r.ran, "a timed call must not also go through the untimed path")

	// Without a timeout it runs plainly (the single-unit request path).
	r2 := newFakeRunner()
	New(r2).IsActive(0, "hostit-app@a")
	assert.Equal(t, []string{"systemctl is-active hostit-app@a"}, r2.ran)
	assert.Empty(t, r2.timed)
}
