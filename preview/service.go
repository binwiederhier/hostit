// Package preview schedules and stores dashboard screenshots of running apps
// for the "screenshot" app-preview mode. A periodic sweep re-shoots everything,
// and assistant activity schedules a debounced, rate-limited shot so the card
// catches up shortly after a change. Shots are plain PNGs on control's local
// disk, keyed by app id so renames keep their screenshot.
//
// The machine work -- running chrome in a locked-down container and the per-shot
// egress firewall -- happens on the node the app lives on, behind the NodeAgent
// Screenshot verb (see node/screenshot). This package owns only the control-side
// half: which apps to shoot and when (the sweep, the debounce, the rate limit),
// the egress policy it passes down in each spec, and storing and pruning the PNGs
// the node hands back. One shot runs at a time, through a single queue.
package preview

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/node/api"
)

const (
	// SweepInterval is how often every running app is re-shot
	SweepInterval = 6 * time.Hour
	// debounceDelay is how long after the LAST assistant change a shot fires
	debounceDelay = time.Minute
	// bucketCapacity caps assistant-triggered shots per app per hour
	bucketCapacity = 5
	// queueSize bounds the shot queue; beyond it, requests are dropped with a warning
	queueSize = 64
	// dirName is where shots live, under control's data dir
	dirName = "previews"
)

// Shooter renders one app preview on the node the app lives on and returns the
// PNG bytes. It is the one machine-facing dependency of this scheduler: in
// production it crosses the cluster link to the app's node (control's NodeAgent
// Screenshot verb); in tests it is a stub.
type Shooter func(spec *api.ScreenshotSpec) ([]byte, error)

// App is one candidate for a screenshot.
type App struct {
	ID      string // Stable identity; names the screenshot file
	Name    string
	URL     string
	Running bool // Only running apps are shot; stopped ones keep their last shot
	// Private restricts the app's URL. It is shot only when the Manager has a
	// preview-cookie minter (SetPreviewCookie): the shot browser then presents an
	// app-bound grant so the proxy serves the app instead of the refusal page.
	Private bool
}

// bucket is one app's token bucket: bucketCapacity tokens, refilled linearly
// over an hour.
type bucket struct {
	tokens float64
	last   time.Time
}

// Manager shoots apps through a single worker queue.
type Manager struct {
	shoot      Shooter
	dir        string
	apps       func() ([]App, error) // Current apps, running or not
	debounce   time.Duration
	queue      chan App
	timers     map[string]*time.Timer // Pending debounce per app name
	buckets    map[string]*bucket     // Rate limit per app id
	now        func() time.Time       // Injectable clock for the bucket tests
	isolate    bool                   // Strict egress isolation (default off; on in screenshot mode)
	allowCIDRs []string               // Extra destinations allowed in strict mode
	// cookie mints the auth cookie the shot browser presents so a PRIVATE app is
	// served to it rather than the sign-in page. nil (the default) means private
	// apps are not shot at all -- an unauthenticated shot would photograph the
	// refusal page, so it is dropped instead.
	cookie func(a App) *http.Cookie
	mu     sync.Mutex // Protects timers, buckets
}

// Dir returns where shots live for a given control data dir.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, dirName)
}

// New returns a Manager that renders shots through shoot and stores them in dir;
// apps lists the current apps.
func New(shoot Shooter, dir string, apps func() ([]App, error)) *Manager {
	return &Manager{
		shoot:    shoot,
		dir:      dir,
		apps:     apps,
		debounce: debounceDelay,
		queue:    make(chan App, queueSize),
		timers:   make(map[string]*time.Timer),
		buckets:  make(map[string]*bucket),
		now:      time.Now,
	}
}

// SetIsolation turns on strict egress isolation and records the operator's
// extra allowed destination CIDRs; both travel to the node in each shot spec.
func (m *Manager) SetIsolation(on bool, allowCIDRs []string) {
	m.isolate = on
	m.allowCIDRs = allowCIDRs
}

// SetPreviewCookie supplies the minter for the per-app auth cookie the shot
// browser presents, which is what lets a PRIVATE app be screenshotted. Without
// it, private apps are skipped.
func (m *Manager) SetPreviewCookie(fn func(a App) *http.Cookie) {
	m.cookie = fn
}

// File returns the screenshot path for an app id; the file may not exist yet.
func (m *Manager) File(id string) string {
	return filepath.Join(m.dir, id+".png")
}

// Loop sweeps immediately and every interval, until done closes. The worker it
// starts is the only thing that shoots. The chrome image pull and the isolated
// network are the node's to prepare, on its first shot.
func (m *Manager) Loop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting app preview screenshot loop", "interval", interval)
	defer slog.Info("Stopping app preview screenshot loop")
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

// Refresh queues a shot of the named app right now (the dashboard's manual
// refresh button), bypassing the debounce and the rate limit. It still goes
// through the single queue, so it runs one at a time like every other shot.
func (m *Manager) Refresh(name string) {
	apps, err := m.apps()
	if err != nil {
		slog.Warn("Cannot list apps for a manual preview refresh", "app", name, "error", err)
		return
	}
	for _, a := range apps {
		if a.Name == name {
			// Deliberately NO Running check: the state cache lags a brand-new app
			// by a few seconds, and a manual refresh silently dropped on stale
			// state looks broken (the API already said 202). Worst case the shot
			// captures a down app's error page -- which the dashboard does not
			// show for a non-previewable app anyway.
			m.enqueue(a)
			return
		}
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
	// The one place every shot passes through -- the sweep, a debounced change
	// and a manual refresh all land here. A private app is shot only when a
	// preview-cookie minter is configured (so the browser can authenticate);
	// without one it is dropped, since an unauthenticated shot photographs the
	// refusal page.
	if a.Private && m.cookie == nil {
		return
	}
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
			if err := m.render(a); err != nil {
				slog.Warn("Cannot screenshot app", "app", a.Name, "url", a.URL, "error", err)
			}
		case <-done:
			return
		}
	}
}

// render asks the app's node for a shot and moves it into place, so a failed or
// half-written shot never replaces the last good one.
func (m *Manager) render(a App) error {
	spec := &api.ScreenshotSpec{
		Name:       a.Name,
		URL:        a.URL,
		Isolate:    m.isolate,
		AllowCIDRs: m.allowCIDRs,
	}
	// A private app carries its app-bound grant cookie so the proxy serves the
	// app to the shot browser, not the sign-in page. enqueue already dropped
	// private apps when no minter is configured; this is the belt.
	if a.Private && m.cookie != nil {
		if c := m.cookie(a); c != nil {
			spec.CookieName, spec.CookieValue, spec.CookieSecure = c.Name, c.Value, c.Secure
		}
	}
	png, err := m.shoot(spec)
	if err != nil {
		return err
	}
	// Write beside the target and rename, so a reader never sees half a PNG.
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	tmp := m.File(a.ID) + ".tmp"
	if err := os.WriteFile(tmp, png, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.File(a.ID))
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
