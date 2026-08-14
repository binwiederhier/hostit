// Package preview takes periodic screenshots of running apps with headless
// chromium, for the web UI's "screenshot" app-preview mode. Shots are plain
// PNGs on local disk, keyed by app id so renames keep their screenshot.
package preview

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/run"
)

const (
	// SweepInterval is how often every running app is re-shot
	SweepInterval = 6 * time.Hour
	// screenshotTimeout bounds one chromium run; a hung page must not stall the sweep
	screenshotTimeout = 60 * time.Second
	// windowSize is the shot's viewport, matching the web UI's preview pane ratio
	windowSize = "1280,800"
	// dirName is where shots live, under the daemon's data dir
	dirName = "previews"
)

var (
	// chromiumBinaries are the names probed on PATH, first hit wins
	chromiumBinaries = []string{"chromium", "chromium-browser", "google-chrome"}
)

// App is one candidate for a screenshot sweep.
type App struct {
	ID      string // Stable identity; names the screenshot file
	Name    string
	URL     string
	Running bool // Only running apps are shot; stopped ones keep their last shot
}

// Manager sweeps running apps with headless chromium and stores the shots.
type Manager struct {
	runner   run.Runner
	dir      string
	apps     func() ([]App, error)        // Current apps, running or not
	lookPath func(string) (string, error) // exec.LookPath, injectable in tests
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
		lookPath: exec.LookPath,
	}
}

// File returns the screenshot path for an app id; the file may not exist yet.
func (m *Manager) File(id string) string {
	return filepath.Join(m.dir, id+".png")
}

// Loop sweeps immediately and then every interval, until done closes.
func (m *Manager) Loop(interval time.Duration, done <-chan struct{}) {
	slog.Info("Starting app preview screenshot loop", "interval", interval)
	defer slog.Info("Stopping app preview screenshot loop")
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

// Sweep screenshots every running app and prunes shots of deleted apps. A
// failed shot keeps the previous one; chromium missing skips the sweep whole.
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
	binary := m.binary()
	if binary == "" {
		slog.Warn("No chromium found for preview screenshots; install chromium, chromium-browser or google-chrome")
		return
	}
	for _, a := range apps {
		if !a.Running {
			continue
		}
		if err := m.screenshot(binary, a); err != nil {
			slog.Warn("Cannot screenshot app", "app", a.Name, "url", a.URL, "error", err)
		}
	}
}

// screenshot shoots one app into a temp file and moves it into place, so a
// failed or half-written shot never replaces the last good one.
func (m *Manager) screenshot(binary string, a App) error {
	// The temp name must keep the .png suffix: chrome validates the extension
	// and writes nothing (exiting 0) for an unknown one
	tmp := filepath.Join(m.dir, a.ID+".tmp.png")
	defer os.Remove(tmp)
	_, err := m.runner.RunTimeout(screenshotTimeout, binary,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
		"--window-size="+windowSize, "--virtual-time-budget=10000",
		"--screenshot="+tmp, a.URL)
	if err != nil {
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

// binary returns the first chromium-family binary on PATH, or "".
func (m *Manager) binary() string {
	for _, name := range chromiumBinaries {
		if path, err := m.lookPath(name); err == nil {
			return path
		}
	}
	return ""
}
