// Package appctl implements the app-side deploy logic behind "hostit up" and
// friends: it reads the app's hostit.yml, asks the daemon who the caller is via
// the unix socket, and manages the app's systemd user unit (optionally wrapping
// rootless podman for container mode).
package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/config"
)

const (
	// selfRequestTimeout bounds the /v1/self call to the daemon socket
	selfRequestTimeout = 5 * time.Second
)

// Runner executes external commands; the real implementation shells out, tests fake it
type Runner interface {
	Run(name string, args ...string) (string, error)
}

// Controller deploys and controls the calling user's app
type Controller struct {
	appDir     string
	socketFile string
	runner     Runner
	self       *SelfInfo // Cached daemon identity lookup
	podman     string    // Resolved podman binary path
}

// NewController creates a Controller for the app living in appDir (usually $HOME)
func NewController(appDir, socketFile string, runner Runner) *Controller {
	return &Controller{
		appDir:     appDir,
		socketFile: socketFile,
		runner:     runner,
	}
}

// Up (re)deploys the app: it builds the image if requested, writes the systemd
// user unit and (re)starts the service
func (c *Controller) Up() error {
	self, err := c.Self()
	if err != nil {
		return err
	}
	conf, err := LoadAppConfig(filepath.Join(c.appDir, "hostit.yml"))
	if err != nil {
		return err
	}

	// Build the image first if a build context is configured
	if conf.Build != "" {
		podman, err := c.podmanPath()
		if err != nil {
			return err
		}
		buildDir := conf.Build
		if !filepath.IsAbs(buildDir) {
			buildDir = filepath.Join(c.appDir, buildDir)
		}
		if _, err := c.runner.Run(podman, "build", "--tag", buildImageTag, buildDir); err != nil {
			return fmt.Errorf("image build failed: %w", err)
		}
	}

	// Write the unit and (re)start the service; enable makes it start at boot
	podman := ""
	if conf.Mode() == ModeContainer {
		if podman, err = c.podmanPath(); err != nil {
			return err
		}
	}
	unitFilename := filepath.Join(c.appDir, ".config", "systemd", "user", unitName+".service")
	if err := os.MkdirAll(filepath.Dir(unitFilename), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitFilename, []byte(unitFile(conf, self, c.appDir, podman)), 0o644); err != nil {
		return err
	}
	if _, err := c.runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := c.runner.Run("systemctl", "--user", "enable", unitName); err != nil {
		return err
	}
	if _, err := c.runner.Run("systemctl", "--user", "restart", unitName); err != nil {
		return err
	}
	return nil
}

// Down stops the app and disables it at boot
func (c *Controller) Down() error {
	_, err := c.runner.Run("systemctl", "--user", "disable", "--now", unitName)
	return err
}

// Restart restarts the app service
func (c *Controller) Restart() error {
	_, err := c.runner.Run("systemctl", "--user", "restart", unitName)
	return err
}

// Status returns the systemd status output for the app service
func (c *Controller) Status() (string, error) {
	return c.runner.Run("systemctl", "--user", "status", "--no-pager", unitName)
}

// Logs streams the app's journal to the terminal; it inherits stdio so that
// --follow behaves like plain journalctl
func (c *Controller) Logs(follow bool, lines int) error {
	args := []string{"--user", "--unit", unitName, "--no-pager"}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	if follow {
		args = append(args, "--follow")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Env = userSessionEnv()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Self returns the daemon's view of the calling app (name, port, URL), cached
func (c *Controller) Self() (*SelfInfo, error) {
	if c.self != nil {
		return c.self, nil
	}
	self, err := Self(c.socketFile)
	if err != nil {
		return nil, err
	}
	c.self = self
	return self, nil
}

func (c *Controller) podmanPath() (string, error) {
	if c.podman != "" {
		return c.podman, nil
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		return "", fmt.Errorf("podman not found in PATH; install podman for container mode: %w", err)
	}
	c.podman = podman
	return podman, nil
}

// Self asks the hostit daemon over its unix socket who the calling user is; the
// daemon authenticates the caller via SO_PEERCRED, so no token is needed
func Self(socketFile string) (*SelfInfo, error) {
	client := &http.Client{
		Timeout: selfRequestTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketFile)
			},
		},
	}
	resp, err := client.Get("http://hostit/v1/self")
	if err != nil {
		return nil, fmt.Errorf("cannot reach hostit daemon at %s: %w", socketFile, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hostit daemon rejected identity lookup (HTTP %d); are you logged in as an app user?", resp.StatusCode)
	}
	var self SelfInfo
	if err := json.NewDecoder(resp.Body).Decode(&self); err != nil {
		return nil, err
	}
	return &self, nil
}

// NewRunner returns the real command runner. It ensures XDG_RUNTIME_DIR and the
// session bus address are set, which "systemctl --user" needs over SSH.
func NewRunner() Runner {
	return &execRunner{}
}

// DefaultSocketFile returns the daemon socket path from the default config
func DefaultSocketFile() string {
	return config.NewConfig().SocketFile
}

// execRunner is the real Runner; it inherits stdio so the user sees tool output
type execRunner struct{}

var _ Runner = (*execRunner)(nil)

func (e *execRunner) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = userSessionEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// userSessionEnv returns the current environment plus the XDG/DBus variables
// required for systemctl --user, in case the SSH session did not set them
func userSessionEnv() []string {
	env := os.Environ()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
		env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+runtimeDir+"/bus")
	}
	return env
}
