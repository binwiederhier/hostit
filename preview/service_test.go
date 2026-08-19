package preview

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	fail     bool
	failNft  bool
	cmds     [][]string
	nftRules string     // Contents of every nft -f ruleset applied
	mu       sync.Mutex // Protects cmds, nftRules
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
	if len(args) > 0 && args[0] == "nft" {
		r.mu.Lock()
		fn := r.failNft
		r.mu.Unlock()
		if fn {
			return "", fmt.Errorf("nft blew up")
		}
		// Capture the ruleset content (nft -f <path>) so tests can inspect it
		if len(args) == 3 && args[1] == "-f" {
			if b, err := os.ReadFile(args[2]); err == nil {
				r.mu.Lock()
				r.nftRules += string(b)
				r.mu.Unlock()
			}
		}
		return "", nil
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

// ran reports whether any recorded command, or applied nft ruleset, contains sub.
func (r *fakeRunner) ran(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(r.nftRules, sub) {
		return true
	}
	for _, c := range r.cmds {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
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
	assert.Contains(t, cmd, "--uidmap=0:3000000:2000000")
	assert.Contains(t, cmd, "--gidmap=0:3000000:2000000")
	assert.Contains(t, cmd, "--cap-drop=ALL")
	assert.Contains(t, cmd, "--headless")
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

// strictManager is a manager in strict isolation with a fixed resolver.
func strictManager(t *testing.T, runner *fakeRunner, apps []App, ips ...string) *Manager {
	t.Helper()
	m := newTestManager(t, runner, apps)
	resolved := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		resolved = append(resolved, net.ParseIP(ip))
	}
	m.SetIsolation(true, nil)
	m.lookupIP = func(string) ([]net.IP, error) { return resolved, nil }
	return m
}

func TestStrictShotResolvesPinsAndFirewalls(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := strictManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	}, "203.0.113.5")
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	// The egress firewall was set before the shot, allowing exactly the resolved IP
	assert.True(t, runner.ran("nft -f"), "an nft ruleset is applied")
	assert.True(t, runner.ran("203.0.113.5"), "the resolved app IP is in the ruleset")
	// The container is put on the isolated network, pinned to that IP, with public DNS
	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "--network hostit-preview")
	assert.Contains(t, cmd, "--add-host up.example.com:203.0.113.5")
	assert.Contains(t, cmd, "--dns")
}

func TestStrictAllowsExtraCIDRs(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	m.SetIsolation(true, []string{"192.0.2.0/24"})
	m.lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("198.51.100.7")}, nil }
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.True(t, runner.ran("192.0.2.0/24"), "the operator's extra allow CIDR is in the ruleset")
}

func TestStrictFailsClosedWhenFirewallFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{failNft: true}
	m := strictManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	}, "203.0.113.5")
	m.Sweep()
	require.Eventually(t, func() bool { return runner.ran("nft -f") }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, runner.shots(), "no container runs if the egress firewall could not be applied")
	assert.NoFileExists(t, m.File("aaa"))
}

func TestStrictSkipsWhenResolveFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	m.SetIsolation(true, nil)
	m.lookupIP = func(string) ([]net.IP, error) { return nil, fmt.Errorf("nxdomain") }
	m.Sweep()
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, runner.shots(), "an unresolvable app is not shot")
	assert.False(t, runner.ran("nft -f"))
}

func TestOffModeRunsWithoutIsolation(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	// Default is off: no firewall, no isolated network
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.False(t, runner.ran("nft -f"))
	assert.NotContains(t, strings.Join(runner.lastShot(), " "), "--network hostit-preview")
}

func TestRefreshEnqueuesEvenWhenTheCacheSaysStopped(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	// The state cache lags a brand-new app by a few seconds: Running=false here
	// even though the app is up. A manual refresh must not be silently dropped.
	m := newTestManager(t, runner, []App{
		{ID: "aaa", Name: "fresh", URL: "https://fresh.example.com", Running: false},
	})
	m.Refresh("fresh")
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)
}

// A blank white card means the shot was taken before the page painted. Two
// flags prevent that and are easy to lose in a refactor, so they are pinned:
// the virtual-time budget gives a slow app time to render (chrome pauses that
// clock while network fetches are outstanding, so this is a rendering budget,
// not a network timeout), and running all compositor stages before the draw
// stops the capture happening mid-paint.
func TestShotWaitsForThePageToRender(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	m := newTestManager(t, runner, []App{{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true}})
	m.Sweep()
	require.Eventually(t, func() bool { return runner.shots() == 1 }, 5*time.Second, 5*time.Millisecond)

	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "--virtual-time-budget="+virtualTimeBudgetMS)
	assert.Contains(t, cmd, "--run-all-compositor-stages-before-draw")

	budget, err := strconv.Atoi(virtualTimeBudgetMS)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, budget, 20000, "a slow app needs more than a couple of seconds to paint")
	assert.Less(t, time.Duration(budget)*time.Millisecond, screenshotTimeout,
		"the budget must fit inside the container timeout, or the run is killed mid-render")
}
