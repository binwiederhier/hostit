// Package screenshot is the node-side engine that renders one app preview: it
// runs the headless-shell chrome in a locked-down rootful podman container,
// drives it over the DevTools protocol, and returns the PNG bytes. It runs on
// the node an app physically lives on, behind the NodeAgent Screenshot verb;
// control keeps the scheduling, the rate limiting, the storage and the serving
// (see package preview).
//
// The page content is untrusted (an app can serve anything, including a
// renderer exploit), so chrome never runs on the host: every shot runs in its
// own user namespace via an explicit high uid/gid mapping, all capabilities
// dropped, no privilege escalation, memory and pid caps -- swap pinned equal to
// the memory cap so a heavy page OOM-kills its own shot instead of thrashing the
// host into a freeze. Chrome's own sandbox is off inside; the container is the
// sandbox. The engine takes one shot per call and its caller (control) runs one
// at a time.
//
// In strict isolation the shot container is put on a dedicated podman network
// and an nftables egress filter, rebuilt per shot, lets it reach only the
// target app's resolved IP (pinned via --add-host) and the public internet --
// the host, the LAN/VPC and the cloud metadata endpoint are dropped. The app's
// IP may itself be private (self-hosted installs), so the allow rule keys on the
// resolved address, not on it being public. If the filter cannot be applied, the
// shot fails (fail closed).
package screenshot

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/node/api"
	"heckel.io/hostit/system/run"
)

const (
	// image is the chrome the shots run in; pulled on the first shot. Chosen for
	// supporting one-shot --screenshot runs (chromedp/headless-shell does not:
	// its build only serves the CDP protocol) and for running chrome as a
	// non-root user inside the container.
	image = "docker.io/zenika/alpine-chrome:latest"
	// screenshotTimeout bounds one whole shot -- chrome starting, the page
	// loading, the settle, the capture -- so a hung page never stalls the queue.
	screenshotTimeout = 120 * time.Second
	// settleDelay is the CEILING on the settle after the load event: the capture
	// polls (see settlePoll) and returns as soon as two consecutive frames match
	// and are non-blank -- the page has finished painting and stopped changing.
	// A fixed delay could not do this; it shot whatever was on screen at one
	// instant, so a still-painting page came out blank or half-rendered. Only a
	// page that never settles (an animating game) waits the whole ceiling.
	settleDelay = 20 * time.Second
	// settlePoll is how often the settle re-captures to check the frame stopped
	// changing; the shot returns as soon as two consecutive frames match, so a
	// static page is quick and settleDelay is only the ceiling for a busy one.
	settlePoll = 1500 * time.Millisecond
	// readyTimeout bounds the preflight that checks the app is actually
	// serving. An app whose container is up but whose server has not bound yet
	// would otherwise be photographed as a connection error -- a white card
	// that then sits there until the next sweep.
	readyTimeout = 10 * time.Second
	// debugPort is chrome's DevTools port inside the container; it is published
	// to the host's loopback only, on a port picked per shot.
	debugPort = "9222"
	// pullTimeout bounds the one-off image pull on the first shot
	pullTimeout = 10 * time.Minute
	// windowSize is the shot's layout viewport (desktop), matching the dashboard
	// card's ratio; deviceScaleFactor then renders it at half resolution so the
	// stored PNG is ~1/4 the pixels (the card shows it small anyway).
	windowSize        = "1280,800"
	deviceScaleFactor = "0.5"
	// workDirName is the per-shot scratch space (the egress ruleset), under the
	// engine's scratch dir.
	workDirName = ".work"
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

// Engine renders one preview per Shoot call. It holds only machine wiring: a
// command runner and a scratch dir. The lookupIP/ready/capture fields are
// injectable so the tests can drive the whole shot path without a network or a
// browser.
type Engine struct {
	runner   run.Runner
	scratch  string                         // Scratch dir for the per-shot egress ruleset
	lookupIP func(string) ([]net.IP, error) // Injectable resolver for the target app
	ready    func(url string) error         // Checks the app is serving before chrome starts
	capture  func(ctx context.Context, debugBase, pageURL string, settle time.Duration, cookie *http.Cookie) ([]byte, error)

	prepareOnce sync.Once // Guards the one-off image pull + orphan cleanup
	networkOnce sync.Once // Guards the one-off isolated network creation
}

// NewEngine returns an Engine that runs shots through runner and keeps its
// per-shot scratch under scratch.
func NewEngine(runner run.Runner, scratch string) *Engine {
	return &Engine{
		runner:   runner,
		scratch:  scratch,
		lookupIP: net.LookupIP,
		ready:    appIsServing,
		capture:  capture,
	}
}

// prepare is the one-off setup done on the first shot: pull the chrome image and
// clear any shot container orphaned by a previous run (a daemon restart mid-shot
// leaves conmon holding it, since --rm only fires on clean exit).
func (e *Engine) prepare() {
	if _, err := e.runner.RunTimeout(pullTimeout, "podman", "pull", "-q", image); err != nil {
		slog.Warn("Cannot pull the preview screenshot image; shots will fail until it is available", "image", image, "error", err)
	}
	_, _ = e.runner.Run("podman", "rm", "-f", "-t", "0", containerName)
}

// ensureNetwork creates the isolated shot network once, on the first strict
// shot. If it is missing, strict shots fail closed (podman run --network errors,
// so no chrome starts).
func (e *Engine) ensureNetwork() {
	if _, err := e.runner.Run("podman", "network", "inspect", networkName); err != nil {
		// --disable-dns: without it the container's resolver is the gateway
		// (10.89.0.1), which the egress drop blocks; disabling aardvark-dns lets
		// the pinned --dns (public) resolver take effect in resolv.conf instead.
		if _, err := e.runner.Run("podman", "network", "create", "--disable-dns", "--subnet", previewSubnet, networkName); err != nil {
			slog.Warn("Cannot create the isolated preview network; strict shots will be skipped", "network", networkName, "error", err)
		}
	}
}

// Shoot renders one app preview and returns the PNG bytes. It never touches
// disk: control stores what it returns. A failed shot returns an error and no
// bytes, so control keeps whatever card it had.
func (e *Engine) Shoot(spec *api.ScreenshotSpec) ([]byte, error) {
	e.prepareOnce.Do(e.prepare)
	// Preflight: is the app actually serving? A container that is up but whose
	// server has not bound yet renders as a connection error, and storing that
	// replaces a good card with a white one until the next sweep. Failing here
	// keeps whatever control had.
	if err := e.ready(spec.URL); err != nil {
		return nil, fmt.Errorf("app is not serving yet: %w", err)
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, err
	}
	userns := fmt.Sprintf("0:%d:%d", userNSBase, userNSSize)
	args := []string{"podman", "run", "--rm", "--replace", "--detach", "--name", containerName,
		"--uidmap=" + userns, "--gidmap=" + userns,
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--memory=512m", "--memory-swap=512m", "--pids-limit=256", "--shm-size=128m",
		// The DevTools port, reachable from this host's loopback only.
		"--publish", fmt.Sprintf("127.0.0.1:%d:%s", port, debugPort)}
	if spec.Isolate {
		// Resolve the target, install an egress filter that allows only that IP
		// (plus the public internet), and pin the hostname to the resolved IP so
		// chrome and the firewall agree. Any failure here fails the shot.
		e.networkOnce.Do(e.ensureNetwork)
		host, ips, err := e.resolveTarget(spec.URL)
		if err != nil {
			return nil, err
		}
		if err := e.applyEgress(ips, spec.AllowCIDRs); err != nil {
			return nil, fmt.Errorf("preview egress firewall: %w", err)
		}
		args = append(args, "--network", networkName, "--dns", publicDNS1, "--dns", publicDNS2)
		for _, ip := range ips {
			args = append(args, "--add-host", host+":"+ip.String())
		}
	}
	args = append(args, image,
		"--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
		"--window-size="+windowSize, "--force-device-scale-factor="+deviceScaleFactor,
		// Without this the capture can happen mid-paint, which is the other way
		// a card comes out white even though the page had rendered.
		"--run-all-compositor-stages-before-draw",
		"--remote-debugging-address=0.0.0.0", "--remote-debugging-port="+debugPort,
		"about:blank")
	if _, err := e.runner.RunTimeout(screenshotTimeout, args...); err != nil {
		_, _ = e.runner.Run("podman", "rm", "-f", "-t", "0", containerName)
		return nil, err
	}
	// Whatever happens next, the container goes away: it holds the name, a
	// memory cap and a published port until it does.
	defer func() { _, _ = e.runner.Run("podman", "rm", "-f", "-t", "0", containerName) }()

	ctx, cancel := context.WithTimeout(context.Background(), screenshotTimeout)
	defer cancel()
	var cookie *http.Cookie
	if spec.CookieName != "" {
		// App-bound auth so the proxy serves the app, not the sign-in page.
		cookie = &http.Cookie{Name: spec.CookieName, Value: spec.CookieValue, Secure: spec.CookieSecure}
	}
	return e.capture(ctx, fmt.Sprintf("http://127.0.0.1:%d", port), spec.URL, settleDelay, cookie)
}

// appIsServing checks the app answers before chrome is started for it. Any
// HTTP response counts, including a 404 or a 500: the app is up and that IS
// what a visitor would see. Only a failure to connect skips the shot.
func appIsServing(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// freeLoopbackPort asks the kernel for an unused port. There is an inherent
// race between closing it and podman binding it; on a box taking one shot at a
// time it is not worth a retry loop.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// resolveTarget parses the app URL and resolves its host to IPv4 addresses (the
// shot network is v4-only). It errors rather than shoot blind if nothing resolves.
func (e *Engine) resolveTarget(rawURL string) (string, []net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, err
	}
	host := u.Hostname()
	ips, err := e.lookupIP(host)
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
func (e *Engine) applyEgress(ips []net.IP, allowCIDRs []string) error {
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
	if len(allowCIDRs) > 0 {
		fmt.Fprintf(&b, "add rule inet %s forward ip saddr %s ip daddr { %s } accept\n", nftTable, previewSubnet, strings.Join(allowCIDRs, ", "))
	}
	fmt.Fprintf(&b, "add rule inet %s forward ip saddr %s ip daddr { %s } drop\n", nftTable, previewSubnet, internalDropCIDRs)
	path := filepath.Join(e.scratch, workDirName, "egress.nft")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	_, err := e.runner.Run("nft", "-f", path)
	return err
}
