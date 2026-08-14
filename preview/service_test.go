package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner records commands and, for a podman screenshot run, writes the
// file chrome would have written into the bind-mounted work dir.
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
	r.cmds = append(r.cmds, args)
	fail := r.fail
	r.mu.Unlock()
	if fail {
		return "", fmt.Errorf("chrome exploded")
	}
	// Find the -v <host>:/out:U mount and the --screenshot=/out/<file> flag,
	// and write the file where the real container would have.
	var hostDir, file string
	for _, a := range args {
		if h, ok := strings.CutSuffix(a, ":/out:U"); ok {
			hostDir = h
		}
		if p, ok := strings.CutPrefix(a, "--screenshot=/out/"); ok {
			file = p
		}
	}
	if hostDir != "" && file != "" {
		if err := os.WriteFile(filepath.Join(hostDir, file), []byte("png"), 0o600); err != nil {
			return "", err
		}
	}
	return "", nil
}

// shots counts completed screenshot container runs.
func (r *fakeRunner) shots() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.cmds {
		if len(c) > 1 && c[0] == "podman" && c[1] == "run" {
			n++
		}
	}
	return n
}

func (r *fakeRunner) lastShot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.cmds) - 1; i >= 0; i-- {
		if len(r.cmds[i]) > 1 && r.cmds[i][0] == "podman" && r.cmds[i][1] == "run" {
			return r.cmds[i]
		}
	}
	return nil
}

func newTestManager(t *testing.T, runner *fakeRunner, apps []App) *Manager {
	t.Helper()
	m := New(runner, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) {
		return apps, nil
	})
	m.debounce = 20 * time.Millisecond
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go m.worker(done)
	return m
}

func TestSweepScreenshotsRunningAppsInAContainer(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
		{ID: "bbb", Name: "down", URL: "https://down.example.com", Running: false},
	})
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.FileExists(t, m.File("aaa"))
	assert.NoFileExists(t, m.File("bbb"))

	// The shot runs inside a locked-down podman container, not on the host: the
	// page content is untrusted, so the container is the sandbox.
	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "podman run")
	assert.Contains(t, cmd, "--userns=auto")
	assert.Contains(t, cmd, "--cap-drop=ALL")
	assert.Contains(t, cmd, "--security-opt=no-new-privileges")
	assert.Contains(t, cmd, image)
	assert.Contains(t, cmd, "https://up.example.com")
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
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.FileExists(t, m.File("aaa"))
}

func TestFailedShotKeepsTheOldOne(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{fail: true}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	require.NoError(t, os.MkdirAll(m.dir, 0o700))
	require.NoError(t, os.WriteFile(m.File("aaa"), []byte("previous"), 0o600))
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() >= 1 }, 5*time.Second, 5*time.Millisecond)
	b, err := os.ReadFile(m.File("aaa"))
	require.NoError(t, err)
	assert.Equal(t, "previous", string(b), "a failed shot must not clobber the last good one")
}

func TestScheduleShootsOnceAfterTheQuietPeriod(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	// A burst of assistant changes collapses into ONE shot, taken after the
	// debounce window of quiet.
	m.Schedule("up")
	m.Schedule("up")
	m.Schedule("up")
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(3 * m.debounce)
	assert.Equal(t, 1, runner.shots(), "three quick changes must produce one shot")
}

func TestScheduleIgnoresUnknownAndStoppedApps(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "bbb", Name: "down", URL: "https://down.example.com", Running: false},
	})
	m.Schedule("down")
	m.Schedule("ghost")
	time.Sleep(4 * m.debounce)
	assert.Zero(t, runner.shots())
}

func TestScheduleIsRateLimitedPerApp(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	m.debounce = time.Millisecond
	for i := 0; i < bucketCapacity+2; i++ {
		m.Schedule("up")
		time.Sleep(10 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return runner.shots() == bucketCapacity }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, bucketCapacity, runner.shots(), "the bucket caps assistant-triggered shots")
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	t.Parallel()
	m := New(&fakeRunner{}, t.TempDir(), func() ([]App, error) { return nil, nil })
	now := time.Now()
	m.now = func() time.Time { return now }
	for i := 0; i < bucketCapacity; i++ {
		assert.True(t, m.takeToken("aaa"))
	}
	assert.False(t, m.takeToken("aaa"), "the bucket is empty")
	assert.True(t, m.takeToken("bbb"), "buckets are per app")
	// One refill interval later there is exactly one token again
	now = now.Add(time.Hour / bucketCapacity)
	assert.True(t, m.takeToken("aaa"))
	assert.False(t, m.takeToken("aaa"))
}
