// Package preview takes screenshots of running apps for the dashboard's
// "screenshot" app-preview mode: a periodic sweep re-shoots everything, and
// assistant activity schedules a debounced, rate-limited shot so the card
// catches up shortly after a change. Shots are plain PNGs on local disk,
// keyed by app id so renames keep their screenshot.
//
// The page content is untrusted (an app can serve anything, including a
// renderer exploit), so chrome never runs on the host: every shot runs the
// headless-shell image in a locked-down rootful podman container (its own
// user namespace via --userns=auto, all capabilities dropped, no privilege
// escalation, memory and pid caps). Chrome's own sandbox is off inside; the
// container is the sandbox. One shot runs at a time, through a single queue.
package preview

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/run"
)

const (
	// SweepInterval is how often every running app is re-shot
	SweepInterval = 6 * time.Hour
	// image is the headless chrome the shots run in; pulled at loop start
	image = "docker.io/chromedp/headless-shell:latest"
	// screenshotTimeout bounds one container run; a hung page must not stall the queue
	screenshotTimeout = 90 * time.Second
	// pullTimeout bounds the one-off image pull at startup
	pullTimeout = 10 * time.Minute
	// debounceDelay is how long after the LAST assistant change a shot fires
	debounceDelay = time.Minute
	// bucketCapacity caps assistant-triggered shots per app per hour
	bucketCapacity = 5
	// queueSize bounds the shot queue; beyond it, requests are dropped with a warning
	queueSize = 64
	// windowSize is the shot's viewport, matching the dashboard card's ratio
	windowSize = "1280,800"
	// dirName is where shots live, under the daemon's data dir; workDirName is
	// the per-shot scratch space inside it that gets bind-mounted into the container
	dirName     = "previews"
	workDirName = ".work"
	// shotFile is the file name inside the container's output mount
	shotFile = "shot.png"
	// containerPrefix names shot containers, so leftovers are recognizable
	containerPrefix = "hostit-preview-"
)

// App is one candidate for a screenshot.
type App struct {
	ID      string // Stable identity; names the screenshot file
	Name    string
	URL     string
	Running bool // Only running apps are shot; stopped ones keep their last shot
}

// bucket is one app's token bucket: bucketCapacity tokens, refilled linearly
// over an hour.
type bucket struct {
	tokens float64
	last   time.Time
}

// Manager shoots apps through a single worker queue.
type Manager struct {
	runner   run.Runner
	dir      string
	apps     func() ([]App, error) // Current apps, running or not
	debounce time.Duration
	queue    chan App
	timers   map[string]*time.Timer // Pending debounce per app name
	buckets  map[string]*bucket     // Rate limit per app id
	now      func() time.Time       // Injectable clock for the bucket tests
	mu       sync.Mutex             // Protects timers, buckets
}

// Dir returns where shots live for a given daemon data dir.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, dirName)
}

// New returns a Manager storing shots in dir; apps lists the current apps.
func New(runner run.Runner, dir string, apps func() ([]App, error)) *Manager {
	return &Manager{
		runner:   runner,
		dir:      dir,
		apps:     apps,
		debounce: debounceDelay,
		queue:    make(chan App, queueSize),
		timers:   make(map[string]*time.Timer),
		buckets:  make(map[string]*bucket),
		now:      time.Now,
	}
}

// File returns the screenshot path for an app id; the file may not exist yet.
func (m *Manager) File(id string) string {
	return filepath.Join(m.dir, id+".png")
}

// Loop pulls the chrome image, then sweeps immediately and every interval,
// until done closes. The worker it starts is the only thing that shoots.
func (m *Manager) Loop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting app preview screenshot loop", "interval", interval, "image", image)
	defer slog.Info("Stopping app preview screenshot loop")
	if _, err := m.runner.RunTimeout(pullTimeout, "podman", "pull", "-q", image); err != nil {
		slog.Warn("Cannot pull the preview screenshot image; shots will fail until it is available", "image", image, "error", err)
	}
	go m.worker(done)
	m.Sweep()
	for {
		select {
		case <-time.After(interval):
		case <-done:
			return
		}
		m.Sweep()
	}
}

// Schedule notes that the app just changed and arms (or re-arms) its debounce:
// the shot fires debounce after the LAST change, at most bucketCapacity times
// per hour per app.
func (m *Manager) Schedule(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[name]; ok {
		t.Stop()
	}
	var bt *time.Timer
	bt = time.AfterFunc(m.debounce, func() {
		m.mu.Lock()
		if m.timers[name] == bt {
			delete(m.timers, name)
		}
		m.mu.Unlock()
		m.fire(name)
	})
	m.timers[name] = bt
}

// fire resolves the app behind a debounced change and enqueues its shot, if
// the app still exists, still runs, and has rate-limit budget left.
func (m *Manager) fire(name string) {
	apps, err := m.apps()
	if err != nil {
		slog.Warn("Cannot list apps for a scheduled preview shot", "app", name, "error", err)
		return
	}
	for _, a := range apps {
		if a.Name != name || !a.Running {
			continue
		}
		if !m.takeToken(a.ID) {
			slog.Debug("Preview shot rate-limited", "app", name)
			return
		}
		m.enqueue(a)
		return
	}
}

// takeToken consumes one token from the app's hourly bucket; false means the
// budget is used up for now.
func (m *Manager) takeToken(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	b := m.buckets[id]
	if b == nil {
		b = &bucket{tokens: bucketCapacity, last: now}
		m.buckets[id] = b
	}
	b.tokens = min(bucketCapacity, b.tokens+now.Sub(b.last).Hours()*bucketCapacity)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Sweep enqueues a shot of every running app and prunes shots of deleted
// apps. Sweeps do not consume rate-limit tokens; they are the slow baseline.
func (m *Manager) Sweep() {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		slog.Warn("Cannot create preview dir", "dir", m.dir, "error", err)
		return
	}
	apps, err := m.apps()
	if err != nil {
		slog.Warn("Cannot list apps for preview sweep", "error", err)
		return
	}
	m.prune(apps)
	for _, a := range apps {
		if a.Running {
			m.enqueue(a)
		}
	}
}

// enqueue hands an app to the worker without blocking; a full queue drops the
// request (the next sweep catches up).
func (m *Manager) enqueue(a App) {
	select {
	case m.queue <- a:
	default:
		slog.Warn("Preview queue full, dropping shot", "app", a.Name)
	}
}

// worker is the single consumer of the queue: one shot at a time, ever.
func (m *Manager) worker(done <-chan struct{}) {
	for {
		select {
		case a := <-m.queue:
			if err := m.shoot(a); err != nil {
				slog.Warn("Cannot screenshot app", "app", a.Name, "url", a.URL, "error", err)
			}
		case <-done:
			return
		}
	}
}

// shoot renders one app in a sandboxed container and moves the shot into
// place, so a failed or half-written shot never replaces the last good one.
func (m *Manager) shoot(a App) error {
	// A per-shot scratch dir is bind-mounted as the container's output; :U
	// chowns it to the container's mapped root so chrome can write there.
	workDir := filepath.Join(m.dir, workDirName, a.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	container := containerPrefix + a.ID
	_, err := m.runner.RunTimeout(screenshotTimeout, "podman", "run", "--rm", "--replace", "--name", container,
		"--userns=auto", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--memory=1g", "--pids-limit=256",
		"-v", workDir+":/out:U", image,
		"--no-sandbox", "--disable-gpu", "--hide-scrollbars",
		"--window-size="+windowSize, "--virtual-time-budget=10000",
		"--screenshot=/out/"+shotFile, a.URL)
	if err != nil {
		// A timeout kills the podman client, not necessarily the container; make
		// sure nothing keeps rendering (and holding the name) behind our back.
		_, _ = m.runner.Run("podman", "rm", "-f", "-t", "0", container)
		return err
	}
	return os.Rename(filepath.Join(workDir, shotFile), m.File(a.ID))
}

// prune removes shots that belong to no current app (deleted apps).
func (m *Manager) prune(apps []App) {
	known := make(map[string]bool, len(apps))
	for _, a := range apps {
		known[a.ID] = true
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".png")
		if !ok || known[id] {
			continue
		}
		_ = os.Remove(filepath.Join(m.dir, e.Name()))
	}
}
