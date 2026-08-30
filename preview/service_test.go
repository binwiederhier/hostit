package preview

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/node/api"
)

// fakeShooter stands in for the node: it records every spec it is handed and,
// unless made to fail, returns a byte the Manager can store. The scheduling and
// storage path is what these tests are about; the container and firewall live
// in node/screenshot.
type fakeShooter struct {
	fail  bool
	specs []*api.ScreenshotSpec
	mu    sync.Mutex
}

func (f *fakeShooter) shoot(spec *api.ScreenshotSpec) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.fail {
		return nil, fmt.Errorf("node shot failed")
	}
	return []byte("\x89PNG\r\n\x1a\nshot"), nil
}

func (f *fakeShooter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

func (f *fakeShooter) last() *api.ScreenshotSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return nil
	}
	return f.specs[len(f.specs)-1]
}

func newTestManager(t *testing.T, shooter *fakeShooter, apps []App) *Manager {
	t.Helper()
	m := New(shooter.shoot, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) {
		return apps, nil
	})
	m.debounce = 20 * time.Millisecond
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go m.worker(done)
	return m
}

func TestSweepShootsRunningApps(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
		{ID: "bbb", Name: "down", URL: "https://down.example.com", Running: false},
	})
	m.Sweep()
	require.Eventually(t, func() bool { return shooter.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.FileExists(t, m.File("aaa"), "the running app's shot is stored")
	assert.NoFileExists(t, m.File("bbb"), "a stopped app is not shot")
	// The scheduler hands the node the app's public URL to browse.
	assert.Equal(t, "https://up.example.com", shooter.last().URL)
	assert.Equal(t, "up", shooter.last().Name)
}

func TestSweepPrunesShotsOfDeletedApps(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	require.NoError(t, os.MkdirAll(m.dir, 0o700))
	require.NoError(t, os.WriteFile(m.File("zzz"), []byte("old"), 0o600))
	m.Sweep()
	assert.NoFileExists(t, m.File("zzz"), "the shot of a deleted app is pruned")
	require.Eventually(t, func() bool { return shooter.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.FileExists(t, m.File("aaa"))
}

func TestFailedShotKeepsTheOldOne(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{fail: true}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	require.NoError(t, os.MkdirAll(m.dir, 0o700))
	require.NoError(t, os.WriteFile(m.File("aaa"), []byte("previous"), 0o600))
	m.Sweep()
	require.Eventually(t, func() bool { return shooter.count() >= 1 }, 5*time.Second, 5*time.Millisecond)
	b, err := os.ReadFile(m.File("aaa"))
	require.NoError(t, err)
	assert.Equal(t, "previous", string(b), "a failed shot must not clobber the last good one")
}

func TestScheduleShootsOnceAfterTheQuietPeriod(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	// A burst of assistant changes collapses into ONE shot, taken after the
	// debounce window of quiet.
	m.Schedule("up")
	m.Schedule("up")
	m.Schedule("up")
	require.Eventually(t, func() bool { return shooter.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(3 * m.debounce)
	assert.Equal(t, 1, shooter.count(), "three quick changes must produce one shot")
}

func TestScheduleIgnoresUnknownAndStoppedApps(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "bbb", Name: "down", URL: "https://down.example.com", Running: false},
	})
	m.Schedule("down")
	m.Schedule("ghost")
	time.Sleep(4 * m.debounce)
	assert.Zero(t, shooter.count())
}

func TestScheduleIsRateLimitedPerApp(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true},
	})
	m.debounce = time.Millisecond
	for i := 0; i < bucketCapacity+2; i++ {
		m.Schedule("up")
		time.Sleep(10 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return shooter.count() == bucketCapacity }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, bucketCapacity, shooter.count(), "the bucket caps assistant-triggered shots")
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	t.Parallel()
	m := New((&fakeShooter{}).shoot, t.TempDir(), func() ([]App, error) { return nil, nil })
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

func TestRefreshEnqueuesEvenWhenTheCacheSaysStopped(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	// The state cache lags a brand-new app by a few seconds: Running=false here
	// even though the app is up. A manual refresh must not be silently dropped.
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "fresh", URL: "https://fresh.example.com", Running: false},
	})
	m.Refresh("fresh")
	require.Eventually(t, func() bool { return shooter.count() == 1 }, 5*time.Second, 5*time.Millisecond)
}

// A private app must not be photographed unauthenticated: the shot browser
// would capture the refusal page and publish that as the app's card. So a
// private app is dropped unless a cookie minter can authenticate the shot.
func TestPrivateAppsAreShotOnlyWithACookieMinter(t *testing.T) {
	t.Parallel()
	// This tests enqueue's synchronous gating, so the managers run NO worker --
	// a draining worker would race the queue-length assertions.
	newMgr := func() *Manager {
		return New((&fakeShooter{}).shoot, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) { return nil, nil })
	}

	// No minter configured: a private app is dropped, a public app still goes through.
	m := newMgr()
	m.enqueue(App{ID: "a1", Name: "dash", URL: "https://dash.example.com", Running: true, Private: true})
	assert.Empty(t, m.queue, "a private app is dropped when nothing can authenticate the shot")
	m.enqueue(App{ID: "a2", Name: "blog", URL: "https://blog.example.com", Running: true})
	assert.Len(t, m.queue, 1, "a public app still gets shot")

	// With a minter, the private app is enqueued like any other.
	m2 := newMgr()
	m2.SetPreviewCookie(func(App) *http.Cookie { return &http.Cookie{Name: "x", Value: "y"} })
	m2.enqueue(App{ID: "a3", Name: "secret", URL: "https://secret.example.com", Running: true, Private: true})
	assert.Len(t, m2.queue, 1, "a private app is shot once the browser can authenticate")
}

// The scheduler is what decides egress policy and mints the grant; it must pass
// both to the node in the spec, or the node cannot isolate the shot or reach a
// private app.
func TestShotSpecCarriesIsolationAndGrant(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := newTestManager(t, shooter, []App{
		{ID: "aaa", Name: "secret", URL: "https://secret.example.com", Running: true, Private: true},
	})
	m.SetIsolation(true, []string{"192.0.2.0/24"})
	m.SetPreviewCookie(func(a App) *http.Cookie {
		return &http.Cookie{Name: "hostit_grant", Value: "signed-" + a.Name, Secure: true}
	})
	m.Sweep()
	require.Eventually(t, func() bool { return shooter.count() == 1 }, 5*time.Second, 5*time.Millisecond)

	spec := shooter.last()
	require.NotNil(t, spec)
	assert.True(t, spec.Isolate, "the node is told to isolate the shot")
	assert.Equal(t, []string{"192.0.2.0/24"}, spec.AllowCIDRs, "the operator's extra allow CIDRs travel to the node")
	assert.Equal(t, "hostit_grant", spec.CookieName, "the private app's grant cookie travels to the node")
	assert.Equal(t, "signed-secret", spec.CookieValue)
	assert.True(t, spec.CookieSecure)
}

// The first sweep after startup waits for the node to connect: the shot runs on
// the node over the cluster link, and on a colocated box control restarts and
// would otherwise sweep before the node has re-dialed in, failing every shot
// with "node not connected" and leaving previews stale until the next interval.
func TestLoopDelaysTheFirstSweep(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := New(shooter.shoot, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) {
		return []App{{ID: "aaa", Name: "up", URL: "https://up.example.com", Running: true}}, nil
	})
	m.initialDelay = 500 * time.Millisecond
	done := make(chan struct{})
	defer close(done)
	go m.Loop(time.Hour, done) // long interval: only the initial sweep is in play

	// Well inside the delay (a 5x margin, so a slow scheduler under -race does not
	// let the sweep fire early and flake the test).
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, shooter.count(), "the first sweep must wait for the node to connect")
	require.Eventually(t, func() bool { return shooter.count() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"the first sweep runs once the initial delay elapses")
}

// Stopping during the initial delay exits the loop promptly and shoots nothing.
func TestLoopStopsDuringTheInitialDelay(t *testing.T) {
	t.Parallel()
	shooter := &fakeShooter{}
	m := New(shooter.shoot, filepath.Join(t.TempDir(), "previews"), func() ([]App, error) { return nil, nil })
	m.initialDelay = time.Hour // long enough that we are certainly still waiting
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { m.Loop(time.Hour, done); close(stopped) }()
	close(done)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not exit during the initial delay")
	}
	assert.Zero(t, shooter.count())
}
