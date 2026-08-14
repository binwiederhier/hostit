package preview

import (
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
)

// fakeRunner records commands and writes the file chromium would have written.
type fakeRunner struct {
	fail bool
	cmds [][]string
	mu   sync.Mutex // Protects cmds
}

func (r *fakeRunner) Run(args ...string) (string, error) {
	return r.RunTimeout(0, args...)
}

func (r *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, args)
	if r.fail {
		return "", fmt.Errorf("chromium exploded")
	}
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--screenshot="); ok {
			if err := os.WriteFile(p, []byte("png"), 0o600); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

func newTestManager(t *testing.T, runner *fakeRunner, apps []App) *Manager {
	t.Helper()
	m := New(runner, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) {
		return apps, nil
	})
	m.lookPath = func(string) (string, error) { return "/usr/bin/chromium", nil }
	return m
}

func TestSweepScreenshotsRunningApps(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
		{ID: "bbb", Name: "down", URL: "https://down.example.com", Running: false},
	})
	m.Sweep()

	// Only the running app was shot, and its file is in place
	assert.FileExists(t, m.File("aaa"))
	assert.NoFileExists(t, m.File("bbb"))
	require.Len(t, runner.cmds, 1)
	cmd := strings.Join(runner.cmds[0], " ")
	assert.Contains(t, cmd, "https://up.example.com")
	assert.Contains(t, cmd, "--headless")
	// Chrome validates the --screenshot extension and silently (exit 0!) writes
	// nothing for anything but an image suffix, so the temp file must be a .png
	for _, arg := range runner.cmds[0] {
		if p, ok := strings.CutPrefix(arg, "--screenshot="); ok {
			assert.True(t, strings.HasSuffix(p, ".png"), "temp screenshot path %q must end in .png", p)
		}
	}
}

func TestSweepPrunesShotsOfDeletedApps(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	require.NoError(t, os.MkdirAll(m.dir, 0o700))
	require.NoError(t, os.WriteFile(m.File("zzz"), []byte("old"), 0o600))
	m.Sweep()
	assert.NoFileExists(t, m.File("zzz"), "the shot of a deleted app is pruned")
	assert.FileExists(t, m.File("aaa"))
}

func TestSweepWithoutChromiumDoesNothing(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	m.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	m.Sweep()
	assert.Empty(t, runner.cmds)
	assert.NoFileExists(t, m.File("aaa"))
}

func TestSweepKeepsTheOldShotWhenChromiumFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{fail: true}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	require.NoError(t, os.MkdirAll(m.dir, 0o700))
	require.NoError(t, os.WriteFile(m.File("aaa"), []byte("previous"), 0o600))
	m.Sweep()
	b, err := os.ReadFile(m.File("aaa"))
	require.NoError(t, err)
	assert.Equal(t, "previous", string(b), "a failed shot must not clobber the last good one")
}
