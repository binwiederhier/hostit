package screenshot

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/node/api"
	"heckel.io/hostit/workspace"
)

// fakeRunner records commands; for an nft ruleset it captures the content, and
// it never runs a real container -- the injected capture stands in for chrome.
type fakeRunner struct {
	fail     bool
	failNft  bool
	cmds     [][]string
	nftRules string     // Contents of every nft -f ruleset applied
	mu       sync.Mutex // Protects cmds, nftRules
}

func (r *fakeRunner) Run(args ...string) (string, error) { return r.RunTimeout(0, args...) }

func (r *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, args)
	fail := r.fail
	r.mu.Unlock()
	if len(args) > 0 && args[0] == "nft" {
		r.mu.Lock()
		fn := r.failNft
		r.mu.Unlock()
		if fn {
			return "", fmt.Errorf("nft blew up")
		}
		if len(args) == 3 && args[1] == "-f" {
			if b, err := os.ReadFile(args[2]); err == nil {
				r.mu.Lock()
				r.nftRules += string(b)
				r.mu.Unlock()
			}
		}
		return "", nil
	}
	// The screenshot container run (podman run ... about:blank) is the only one
	// the fail flag applies to; setup commands (pull, rm, network) always pass.
	if fail && len(args) > 1 && args[0] == "podman" && args[1] == "run" {
		return "", fmt.Errorf("chrome exploded")
	}
	return "", nil
}

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

// recordingEngine is an Engine whose ready/capture are stubbed: the app is
// serving and chrome hands back a byte, so the tests exercise the container and
// firewall path without a network or a browser.
func recordingEngine(t *testing.T, runner *fakeRunner) (*Engine, *shotRecord) {
	t.Helper()
	e := NewEngine(runner, filepath.Join(t.TempDir(), "scratch"))
	rec := &shotRecord{}
	e.ready = func(string) error { return nil }
	e.capture = func(_ context.Context, _, pageURL string, _ time.Duration, cookie *http.Cookie) ([]byte, error) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.pageURL = pageURL
		rec.cookie = cookie
		return []byte("\x89PNG\r\n\x1a\nshot"), nil
	}
	return e, rec
}

type shotRecord struct {
	pageURL string
	cookie  *http.Cookie
	mu      sync.Mutex
}

func TestShootRunsInLockedDownContainer(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, rec := recordingEngine(t, runner)
	png, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com"})
	require.NoError(t, err)
	require.NotEmpty(t, png, "a successful shot returns the PNG bytes")

	// The shot runs inside a locked-down podman container, not on the host: the
	// page content is untrusted, so the container is the sandbox.
	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "podman run")
	// Derived, not hardcoded: the range must track workspace.UIDTop, and
	// TestShotUIDRangeSitsAboveEveryAppBlock is what pins where that has to be.
	userns := fmt.Sprintf("0:%d:%d", userNSBase, userNSSize)
	assert.Contains(t, cmd, "--uidmap="+userns)
	assert.Contains(t, cmd, "--gidmap="+userns)
	assert.Contains(t, cmd, "--cap-drop=ALL")
	assert.Contains(t, cmd, "--headless")
	assert.Contains(t, cmd, "--security-opt=no-new-privileges")
	assert.Contains(t, cmd, image)
	// The URL is no longer a command-line argument: chrome starts on about:blank
	// and is navigated over the DevTools protocol.
	assert.Equal(t, "https://up.example.com", rec.pageURL)
}

// A blank white card means the shot was taken before the page painted. Chrome
// is started with its DevTools port open and driven (navigate, load, settle,
// capture) rather than run in one-shot --screenshot mode with a VIRTUAL time
// budget, which fast-forwards timers and shot animated pages before their
// content arrived. These are the pieces that are easy to lose in a refactor.
func TestShootDrivesChromeInRealTime(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com"})
	require.NoError(t, err)

	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "--remote-debugging-port="+debugPort, "the shot is driven, not one-shot")
	assert.Contains(t, cmd, "--detach", "chrome has to outlive the run command that starts it")
	assert.Contains(t, cmd, "127.0.0.1:", "the debug port is published to loopback only, never the LAN")
	assert.Contains(t, cmd, "--run-all-compositor-stages-before-draw")
	assert.NotContains(t, cmd, "--virtual-time-budget", "virtual time is what shot animated pages white")
	assert.NotContains(t, cmd, "--screenshot=", "one-shot mode is what carried the virtual clock")

	assert.GreaterOrEqual(t, settleDelay, 5*time.Second, "a slow app needs real seconds to paint")
	assert.Less(t, settleDelay+readyTimeout, screenshotTimeout,
		"the settle must fit inside the shot timeout, or the run is killed mid-render")
}

// An app that is not answering keeps whatever card it had: photographing a
// connection error would replace a good screenshot with a white one.
func TestShootSkipsAnAppThatIsNotServing(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	e.ready = func(string) error { return assert.AnError }
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com"})
	require.Error(t, err, "a shot of an unreachable app fails instead of storing a white card")
	assert.Zero(t, runner.shots(), "no chrome is started for an app that cannot be reached")
}

func TestFailedShotReturnsAnError(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{fail: true}
	e, _ := recordingEngine(t, runner)
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com"})
	require.Error(t, err, "a failed container run is an error, so control keeps the last good card")
}

func TestStrictShotResolvesPinsAndFirewalls(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	e.lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.5")}, nil }
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com", Isolate: true})
	require.NoError(t, err)

	// The egress firewall was set before the shot, allowing exactly the resolved IP.
	assert.True(t, runner.ran("nft -f"), "an nft ruleset is applied")
	assert.True(t, runner.ran("203.0.113.5"), "the resolved app IP is in the ruleset")
	// The container is put on the isolated network, pinned to that IP, with public DNS.
	cmd := strings.Join(runner.lastShot(), " ")
	assert.Contains(t, cmd, "--network hostit-preview")
	assert.Contains(t, cmd, "--add-host up.example.com:203.0.113.5")
	assert.Contains(t, cmd, "--dns")
}

func TestStrictAllowsExtraCIDRs(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	e.lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("198.51.100.7")}, nil }
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com", Isolate: true, AllowCIDRs: []string{"192.0.2.0/24"}})
	require.NoError(t, err)
	assert.True(t, runner.ran("192.0.2.0/24"), "the operator's extra allow CIDR is in the ruleset")
}

func TestStrictFailsClosedWhenFirewallFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{failNft: true}
	e, _ := recordingEngine(t, runner)
	e.lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.5")}, nil }
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com", Isolate: true})
	require.Error(t, err, "a shot fails closed if the egress firewall could not be applied")
	assert.Zero(t, runner.shots(), "no container runs if the egress firewall could not be applied")
}

func TestStrictFailsWhenResolveFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	e.lookupIP = func(string) ([]net.IP, error) { return nil, fmt.Errorf("nxdomain") }
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com", Isolate: true})
	require.Error(t, err, "an unresolvable app is not shot")
	assert.Zero(t, runner.shots())
	assert.False(t, runner.ran("nft -f"))
}

func TestOffModeRunsWithoutIsolation(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, _ := recordingEngine(t, runner)
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "up", URL: "https://up.example.com"})
	require.NoError(t, err)
	assert.False(t, runner.ran("nft -f"))
	assert.NotContains(t, strings.Join(runner.lastShot(), " "), "--network hostit-preview")
}

// A private app's shot seeds the app-bound grant cookie so the proxy serves the
// app rather than the sign-in page.
func TestPrivateShotSeedsTheGrantCookie(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, rec := recordingEngine(t, runner)
	_, err := e.Shoot(&api.ScreenshotSpec{
		Name: "secret", URL: "https://secret.example.com",
		CookieName: "hostit_grant", CookieValue: "signed-token", CookieSecure: true,
	})
	require.NoError(t, err)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotNil(t, rec.cookie, "the shot browser is handed the grant cookie")
	assert.Equal(t, "hostit_grant", rec.cookie.Name)
	assert.Equal(t, "signed-token", rec.cookie.Value)
	assert.True(t, rec.cookie.Secure)
}

func TestPublicShotSeedsNoCookie(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	e, rec := recordingEngine(t, runner)
	_, err := e.Shoot(&api.ScreenshotSpec{Name: "blog", URL: "https://blog.example.com"})
	require.NoError(t, err)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Nil(t, rec.cookie, "a public app's shot carries no cookie")
}

// The shot container's uid range must sit clear of every app's uid block. An
// app is root inside its own user namespace and can become any host uid in its
// block, so an overlapping range would give some tenant the same host uid as
// the browser that renders OTHER tenants' pages -- and, for a private app,
// holds their app-bound preview grant. The old fixed 3,000,000 base sat inside
// the app space (the blocks for ports 10030-10061), which is what this pins.
func TestShotUIDRangeSitsAboveEveryAppBlock(t *testing.T) {
	t.Parallel()
	require.Greater(t, userNSBase, workspace.UIDTop,
		"the shot range starts above the whole app uid space")
	for port := workspace.PortMin; port <= workspace.PortMax; port++ {
		base := workspace.UIDFor(port)
		top := base + workspace.UIDBlockSize - 1
		require.False(t, top >= userNSBase && base <= userNSBase+userNSSize-1,
			"app on port %d owns uids %d..%d, which overlaps the shot range %d..%d",
			port, base, top, userNSBase, userNSBase+userNSSize-1)
	}
}
