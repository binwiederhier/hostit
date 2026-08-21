// Package preview takes screenshots of running apps for the dashboard's
// "screenshot" app-preview mode: a periodic sweep re-shoots everything, and
// assistant activity schedules a debounced, rate-limited shot so the card
// catches up shortly after a change. Shots are plain PNGs on local disk,
// keyed by app id so renames keep their screenshot.
//
// The page content is untrusted (an app can serve anything, including a
// renderer exploit), so chrome never runs on the host: every shot runs the
// headless-shell image in a locked-down rootful podman container (its own
// user namespace via an explicit high uid/gid mapping, all capabilities
// dropped, no privilege escalation, memory and pid caps -- swap pinned equal to
// the memory cap so a heavy page OOM-kills its own shot instead of thrashing the
// host into a freeze). Chrome's own sandbox
// is off inside; the container is the sandbox. One shot runs at a time, through
// a single queue.
//
// In strict isolation (the default) the shot container is put on a dedicated
// podman network and an nftables egress filter, rebuilt per shot, lets it reach
// only the target app's resolved IP (pinned via --add-host) and the public
// internet -- the host, the LAN/VPC and the cloud metadata endpoint are dropped.
// The app's IP may itself be private (self-hosted installs), so the allow rule
// keys on the resolved address, not on it being public. If the filter cannot be
// applied, the shot is skipped (fail closed).
package preview

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
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
	// image is the chrome the shots run in; pulled at loop start. Chosen for
	// supporting one-shot --screenshot runs (chromedp/headless-shell does not:
	// its build only serves the CDP protocol) and for running chrome as a
	// non-root user inside the container.
	image = "docker.io/zenika/alpine-chrome:latest"
	// screenshotTimeout bounds one container run; a hung page must not stall the
	// queue. It must comfortably exceed the virtual-time budget below, since a
	// page that keeps producing work can consume that budget in real time.
	screenshotTimeout = 150 * time.Second
	// virtualTimeBudgetMS is how long chrome is given to render before the shot.
	// It is a rendering budget rather than a network timeout: chrome pauses this
	// clock while fetches are outstanding, so a slow app does not eat it waiting
	// for its first byte. Ten seconds still produced blank white cards for apps
	// that paint late (a framework booting, fonts, an image), and 25 was still
	// too short for heavy sites; a full minute is generous on purpose, and
	// costs nothing for a page that finishes early -- the shot is taken as soon
	// as the page settles.
	virtualTimeBudgetMS = "60000"
	// pullTimeout bounds the one-off image pull at startup
	pullTimeout = 10 * time.Minute
	// debounceDelay is how long after the LAST assistant change a shot fires
	debounceDelay = time.Minute
	// bucketCapacity caps assistant-triggered shots per app per hour
	bucketCapacity = 5
	// queueSize bounds the shot queue; beyond it, requests are dropped with a warning
	queueSize = 64
	// windowSize is the shot's layout viewport (desktop), matching the dashboard
	// card's ratio; deviceScaleFactor then renders it at half resolution so the
	// stored PNG is ~1/4 the pixels (the card shows it small anyway).
	windowSize        = "1280,800"
	deviceScaleFactor = "0.5"
	// dirName is where shots live, under the daemon's data dir; workDirName is
	// the per-shot scratch space inside it that gets bind-mounted into the container
	dirName     = "previews"
	workDirName = ".work"
	// shotFile is the file name inside the container's output mount
	shotFile = "shot.png"
	// containerName is the single fixed name for the shot container. One shot
	// runs at a time, so a fixed name plus --replace means a new shot always
	// clears any leftover from a prior one (e.g. a daemon restart mid-shot).
	containerName = "hostit-screenshot"
	// userNSBase/userNSSize is the explicit uid/gid mapping for the shot
	// container's user namespace: a high, otherwise-unused host range, mapped
	// directly (rootful podman needs no /etc/subuid for explicit maps).
	userNSBase = 3000000
	userNSSize = 2000000
	// networkName/previewSubnet is the dedicated podman network the shot runs on
	// in strict isolation; the subnet is what the egress nft rules match as source
	networkName   = "hostit-preview"
	previewSubnet = "10.89.0.0/24"
	// nftTable holds the per-shot egress chain
	nftTable = "hostit_preview"
	// internalDropCIDRs are the destinations a shot may never reach: link-local
	// (covers 169.254.169.254 cloud metadata), all RFC1918, and CGNAT. The app's
	// own resolved IP is allowed ahead of this, so a private-IP app still loads.
	internalDropCIDRs = "169.254.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10"
	// publicDNS is pinned on the container so name resolution of third-party
	// (public) assets does not depend on -- or leak to -- an internal resolver
	publicDNS1 = "1.1.1.1"
	publicDNS2 = "8.8.8.8"
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
	runner     run.Runner
	dir        string
	apps       func() ([]App, error) // Current apps, running or not
	debounce   time.Duration
	queue      chan App
	timers     map[string]*time.Timer         // Pending debounce per app name
	buckets    map[string]*bucket             // Rate limit per app id
	now        func() time.Time               // Injectable clock for the bucket tests
	isolate    bool                           // Strict egress isolation (default off; on in screenshot mode)
	allowCIDRs []string                       // Extra destinations allowed in strict mode
	lookupIP   func(string) ([]net.IP, error) // Injectable resolver for the target app
	mu         sync.Mutex                     // Protects timers, buckets
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
		lookupIP: net.LookupIP,
	}
}

// SetIsolation turns on strict egress isolation and records the operator's
// extra allowed destination CIDRs.
func (m *Manager) SetIsolation(on bool, allowCIDRs []string) {
	m.isolate = on
	m.allowCIDRs = allowCIDRs
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
	// Clear any shot container orphaned by a previous run (a daemon restart
	// mid-shot leaves conmon holding it, since --rm only fires on clean exit).
	_, _ = m.runner.Run("podman", "rm", "-f", "-t", "0", containerName)
	if m.isolate {
		// Idempotent: create the isolated network once. If it is missing, strict
		// shots fail closed (podman run --network errors, so no chrome starts).
		if _, err := m.runner.Run("podman", "network", "inspect", networkName); err != nil {
			// --disable-dns: without it the container's resolver is the gateway
			// (10.89.0.1), which the egress drop blocks; disabling aardvark-dns lets
			// the pinned --dns (public) resolver take effect in resolv.conf instead.
			if _, err := m.runner.Run("podman", "network", "create", "--disable-dns", "--subnet", previewSubnet, networkName); err != nil {
				slog.Warn("Cannot create the isolated preview network; strict shots will be skipped", "network", networkName, "error", err)
			}
		}
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
	container := containerName
	userns := fmt.Sprintf("0:%d:%d", userNSBase, userNSSize)
	args := []string{"podman", "run", "--rm", "--replace", "--name", container,
		"--uidmap=" + userns, "--gidmap=" + userns,
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--memory=512m", "--memory-swap=512m", "--pids-limit=256", "--shm-size=128m"}
	if m.isolate {
		// Resolve the target, install an egress filter that allows only that IP
		// (plus the public internet), and pin the hostname to the resolved IP so
		// chrome and the firewall agree. Any failure here skips the shot.
		host, ips, err := m.resolveTarget(a.URL)
		if err != nil {
			return err
		}
		if err := m.applyEgress(ips); err != nil {
			return fmt.Errorf("preview egress firewall: %w", err)
		}
		args = append(args, "--network", networkName, "--dns", publicDNS1, "--dns", publicDNS2)
		for _, ip := range ips {
			args = append(args, "--add-host", host+":"+ip.String())
		}
	}
	args = append(args, "-v", workDir+":/out:U", image,
		"--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
		"--window-size="+windowSize, "--force-device-scale-factor="+deviceScaleFactor,
		"--virtual-time-budget="+virtualTimeBudgetMS,
		// Without this the capture can happen mid-paint, which is the other way
		// a card comes out white even though the page had rendered.
		"--run-all-compositor-stages-before-draw",
		"--screenshot=/out/"+shotFile, a.URL)
	_, err := m.runner.RunTimeout(screenshotTimeout, args...)
	if err != nil {
		// A timeout kills the podman client, not necessarily the container; make
		// sure nothing keeps rendering (and holding the name) behind our back.
		_, _ = m.runner.Run("podman", "rm", "-f", "-t", "0", container)
		return err
	}
	return os.Rename(filepath.Join(workDir, shotFile), m.File(a.ID))
}

// resolveTarget parses the app URL and resolves its host to IPv4 addresses (the
// shot network is v4-only). It errors rather than shoot blind if nothing resolves.
func (m *Manager) resolveTarget(rawURL string) (string, []net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, err
	}
	host := u.Hostname()
	ips, err := m.lookupIP(host)
	if err != nil {
		return "", nil, err
	}
	var v4 []net.IP
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			v4 = append(v4, ip4)
		}
	}
	if len(v4) == 0 {
		return "", nil, fmt.Errorf("no IPv4 address for %s", host)
	}
	return host, v4, nil
}

// applyEgress rebuilds the per-shot nftables egress chain: allow the app's
// resolved IP (on 80/443) and the operator's extra CIDRs, drop everything
// internal, and let the rest (the public internet) fall through. Since one shot
// runs at a time, one dynamic allow rule is always correct.
func (m *Manager) applyEgress(ips []net.IP) error {
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	var b strings.Builder
	// add-then-delete-then-add replaces the table atomically whether or not it existed
	fmt.Fprintf(&b, "add table inet %s\n", nftTable)
	fmt.Fprintf(&b, "delete table inet %s\n", nftTable)
	fmt.Fprintf(&b, "add table inet %s\n", nftTable)
	fmt.Fprintf(&b, "add chain inet %s forward { type filter hook forward priority -10 ; policy accept ; }\n", nftTable)
	fmt.Fprintf(&b, "add rule inet %s forward ip saddr %s ip daddr { %s } tcp dport { 80, 443 } accept\n", nftTable, previewSubnet, strings.Join(strs, ", "))
	if len(m.allowCIDRs) > 0 {
		fmt.Fprintf(&b, "add rule inet %s forward ip saddr %s ip daddr { %s } accept\n", nftTable, previewSubnet, strings.Join(m.allowCIDRs, ", "))
	}
	fmt.Fprintf(&b, "add rule inet %s forward ip saddr %s ip daddr { %s } drop\n", nftTable, previewSubnet, internalDropCIDRs)
	path := filepath.Join(m.dir, workDirName, "egress.nft")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	_, err := m.runner.Run("nft", "-f", path)
	return err
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
