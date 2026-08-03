package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		b, err := os.ReadFile(filepath.Join(home, ".hostit", "app.log"))
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
