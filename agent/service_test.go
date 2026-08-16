package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appconf"
)

func TestAgentRunsAndRestartsCommand(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	writeConfig(t, home, "run: echo mark >> marks.txt; sleep 0.05")
	go func() {
		_ = a.Run()
	}()
	defer a.Stop()
	// The command exits quickly; the agent must restart it, so we see multiple marks
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(home, "marks.txt"))
		return err == nil && strings.Count(string(b), "mark") >= 2
	}, 5*time.Second, 20*time.Millisecond)
}

func TestAgentWritesLogFile(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	writeConfig(t, home, "run: echo hello-from-app; sleep 5")
	go func() {
		_ = a.Run()
	}()
	defer a.Stop()
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(home, appconf.LogDir, "app.log"))
		return err == nil && strings.Contains(string(b), "hello-from-app")
	}, 5*time.Second, 20*time.Millisecond)
}

func TestAgentReload(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	writeConfig(t, home, "run: echo one >> marks.txt; sleep 60")
	go func() {
		_ = a.Run()
	}()
	defer a.Stop()
	waitForFileContains(t, filepath.Join(home, "marks.txt"), "one")
	writeConfig(t, home, "run: echo two >> marks.txt; sleep 60")
	a.Reload()
	waitForFileContains(t, filepath.Join(home, "marks.txt"), "two")
}

func TestAgentPauseAndResume(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	marks := filepath.Join(home, "marks.txt")
	// One "up" line is written each time the command starts, then it sleeps
	writeConfig(t, home, "run: echo up >> marks.txt; sleep 60")
	go func() {
		_ = a.Run()
	}()
	defer a.Stop()
	waitForFileContains(t, marks, "up") // started once

	countUp := func() int {
		b, _ := os.ReadFile(marks)
		return strings.Count(string(b), "up")
	}

	// Pause stops the command and, crucially, keeps it stopped: no restart loop
	a.Pause()
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, countUp(), "a paused app must not restart")

	// Resume starts it again
	a.Resume()
	require.Eventually(t, func() bool { return countUp() >= 2 }, 3*time.Second, 20*time.Millisecond)
}

func TestAgentWritesRunState(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	stateFile := filepath.Join(home, appconf.AppStateFile)
	writeConfig(t, home, "run: sleep 60")
	go func() {
		_ = a.Run()
	}()
	defer a.Stop()

	// The daemon cannot see inside the container, so the agent leaves a breadcrumb
	// the daemon reads to color the status dot.
	waitForFileContains(t, stateFile, "running") // started
	a.Pause()
	waitForFileContains(t, stateFile, "stopped") // paused leaves it stopped
	a.Resume()
	waitForFileContains(t, stateFile, "running") // back up
}

func TestAgentStopKillsChild(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	pidFile := filepath.Join(home, "pid.txt")
	writeConfig(t, home, "run: echo $$ > pid.txt; sleep 60")
	done := make(chan error, 1)
	go func() {
		done <- a.Run()
	}()
	waitForFileContains(t, pidFile, "")
	a.Stop()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop in time")
	}
	// The child must be gone, too
	b, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid := strings.TrimSpace(string(b))
	require.NotEmpty(t, pid)
	assert.Eventually(t, func() bool {
		return !processAlive(t, pid)
	}, 5*time.Second, 20*time.Millisecond)
}

func TestAgentIdleWithoutConfig(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t)
	done := make(chan error, 1)
	go func() {
		done <- a.Run()
	}()
	time.Sleep(100 * time.Millisecond) // Let it settle into idle
	a.Stop()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop in time")
	}
}

func newTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	a := New(home)
	a.restartDelay = 50 * time.Millisecond
	return a, home
}

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(home, "hostit.yml"), []byte(content), 0600))
}

func waitForFileContains(t *testing.T, filename, substr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filename)
		return err == nil && strings.Contains(string(b), substr)
	}, 5*time.Second, 20*time.Millisecond)
}

func processAlive(t *testing.T, pid string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join("/proc", pid))
	return err == nil
}

func TestReportExitNeverBlocks(t *testing.T) {
	t.Parallel()
	// As PID 1 the agent reaps orphans, but nothing drains a.exits while the app
	// is idle (a stub, or a hostit.yml that names no command). A blocking send
	// would stop the reaper for the life of the container and pile up zombies.
	a := New(t.TempDir())
	for i := 0; i < cap(a.exits)+10; i++ {
		done := make(chan struct{})
		go func() {
			a.reportExit(childExit{pid: i, status: "exit status 0"})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("reportExit blocked after %d exits", i)
		}
	}
}

func TestAppLogRotatesWhileRunning(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	a := New(home)
	w, err := a.openLog()
	require.NoError(t, err)
	// A stable app never restarts, so rotation has to happen as it writes;
	// otherwise the log grows forever on a 25 GB disk
	chunk := bytes.Repeat([]byte("x"), 64*1024)
	for written := 0; written < logMaxSize*2; written += len(chunk) {
		_, err := w.Write(chunk)
		require.NoError(t, err)
	}
	stat, err := os.Stat(filepath.Join(home, appconf.LogDir, "app.log"))
	require.NoError(t, err)
	assert.LessOrEqual(t, stat.Size(), int64(logMaxSize), "the live log must stay under the cap")
	_, err = os.Stat(filepath.Join(home, appconf.LogDir, "app.log.old"))
	assert.NoError(t, err, "the previous log must be kept as .old")
}

func TestAgentRunsPrepareBeforeTheCommand(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	// The build step is what makes "keep your source here" workable for someone
	// driving the API with no toolchain of their own
	writeConfig(t, home, "prepare: printf 'built\\n' > artifact.txt\nrun: cat artifact.txt; sleep 5")
	go func() {
		_ = a.Run()
	}()
	t.Cleanup(a.Stop)

	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(home, "artifact.txt"))
		return err == nil && strings.Contains(string(b), "built")
	}, 5*time.Second, 20*time.Millisecond, "prepare must run")
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(home, appconf.LogDir, "app.log"))
		return err == nil && strings.Contains(string(b), "built")
	}, 5*time.Second, 20*time.Millisecond, "the app must see what prepare produced")
}

func TestAgentDoesNotStartWhenPrepareFails(t *testing.T) {
	t.Parallel()
	a, home := newTestAgent(t)
	// A failed build must not be papered over by starting the app anyway
	writeConfig(t, home, "prepare: exit 3\nrun: touch started.txt; sleep 5")
	go func() {
		_ = a.Run()
	}()
	t.Cleanup(a.Stop)

	time.Sleep(300 * time.Millisecond)
	_, err := os.Stat(filepath.Join(home, "started.txt"))
	assert.True(t, os.IsNotExist(err), "the app must not start when its build failed")
}

func TestTimestampWriterPrefixesEachLine(t *testing.T) {
	var buf bytes.Buffer
	w := newTimestampWriter(&buf)

	// A line split across two writes is stamped once, when its newline arrives.
	_, err := w.Write([]byte("hello "))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "a partial line is held until its newline")
	_, err = w.Write([]byte("world\nsecond line\n"))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	stamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `)
	assert.Regexp(t, stamp, lines[0])
	assert.True(t, strings.HasSuffix(lines[0], "hello world"))
	assert.Regexp(t, stamp, lines[1])
	assert.True(t, strings.HasSuffix(lines[1], "second line"))
}

func TestTimestampWriterFlushesAnOverLongLine(t *testing.T) {
	var buf bytes.Buffer
	w := newTimestampWriter(&buf)
	_, err := w.Write(bytes.Repeat([]byte("x"), maxLineBuffer+1))
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "a line past the cap is flushed rather than pinned in memory")
}

func TestRestartPlan(t *testing.T) {
	t.Parallel()
	// A run that lasted a healthy while is not a crash loop: reset the count and
	// restart promptly with the base delay.
	n, d, giveUp := restartPlan(3, healthyRunTime)
	assert.Equal(t, 0, n)
	assert.False(t, giveUp)
	assert.Equal(t, restartBackoffBase, d)

	// Consecutive rapid crashes back off exponentially from the base.
	for _, s := range []struct {
		in    int
		delay time.Duration
	}{
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
	} {
		n, d, giveUp = restartPlan(s.in, time.Second)
		assert.Equal(t, s.in+1, n, "crash count increments")
		assert.False(t, giveUp)
		assert.Equal(t, s.delay, d)
	}

	// After crashLimit rapid crashes in a row, give up so a doomed app stops
	// hammering the box instead of restarting forever.
	n, _, giveUp = restartPlan(crashLimit-1, time.Second)
	assert.True(t, giveUp)
	assert.Equal(t, crashLimit, n)
}

func TestLogNoticeWritesFramedLineToAppLog(t *testing.T) {
	a, home := newTestAgent(t)
	if _, err := a.openLog(); err != nil {
		t.Fatal(err)
	}
	// A hostit-generated notice lands in the app log (Logs tab), timestamped and
	// tagged, so a crash-loop give-up is visible next to the output that caused it.
	a.logNotice("App crashed %d times in a row; giving up.", crashLimit)
	a.closeLog()
	b, err := os.ReadFile(filepath.Join(home, "log", "app.log"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "[hostit] App crashed 5 times in a row; giving up.")
}
