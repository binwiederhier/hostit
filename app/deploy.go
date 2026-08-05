package app

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

const (
	// hostitBinFile is where the hostit binary lives on the host AND inside every
	// app container (bind-mounted), so the CLI works in both worlds
	hostitBinFile = "/usr/bin/hostit"
	// inspectHashFormat extracts the config-hash label from an existing container
	inspectHashFormat = `{{index .Config.Labels "hostit.config"}}`
)

// Up deploys the app from its hostit.yml: builds images as needed, recreates the
// container if its configuration changed, and (re)starts or reloads the service
func (m *Manager) Up(name string) (string, error) {
	defer m.stateChanged(name)
	a, err := m.store.App(name)
	if err != nil {
		return "", err
	}
	conf, err := m.loadConfig(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return m.apply(a, conf, true)
}

// Ensure makes sure the app's container exists and its service is running; it
// never recreates or reloads a live container (used on SSH login). Without a
// (valid) hostit.yml it provisions an idle workspace.
func (m *Manager) Ensure(name string) (string, error) {
	defer m.stateChanged(name)
	a, err := m.store.App(name)
	if err != nil {
		return "", err
	}
	conf, err := m.loadConfig(name)
	if err != nil {
		conf = nil // Fall back to an idle workspace
	}
	if _, err := m.runner.Run("podman", "container", "inspect", containerName(name), "--format", inspectHashFormat); err == nil {
		if m.isActive(name) {
			return "workspace ready", nil
		}
		if _, err := m.runner.Run("systemctl", "enable", "--now", unitName(name)); err != nil {
			return "", err
		}
		return "workspace started", nil
	}
	return m.apply(a, conf, false)
}

// Down stops the app and disables it at boot
func (m *Manager) Down(name string) error {
	defer m.stateChanged(name)
	if _, err := m.store.App(name); err != nil {
		return err
	}
	_, err := m.runner.Run("systemctl", "disable", "--now", unitName(name))
	return err
}

// Restart restarts the app's service (and thus its container)
func (m *Manager) Restart(name string) error {
	defer m.stateChanged(name)
	if _, err := m.store.App(name); err != nil {
		return err
	}
	_, err := m.runner.Run("systemctl", "restart", unitName(name))
	return err
}

// Status returns the systemd status output for the app's service
func (m *Manager) Status(name string) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	out, err := m.runner.Run("systemctl", "status", "--no-pager", unitName(name))
	if out != "" {
		return out, nil // systemctl status exits non-zero for stopped units; still useful
	}
	return out, err
}

// Logs returns the last lines of the app's output: the agent log file for
// workspace apps, podman logs for image apps
func (m *Manager) Logs(name string, lines int) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	// Through the app's root: log/ lives in a directory the app user owns,
	// so the log file can be a symlink to anything the daemon can read
	root, err := m.appRoot(name)
	if err != nil {
		return "", err
	}
	defer root.Close()
	b, err := readCapped(root, appLogFile, maxLogRead)
	if err != nil {
		return "", fmt.Errorf("no logs yet: %w", err)
	}
	return tailLines(string(b), lines), nil
}

// ReconcileOrphans removes the host state of apps that no longer exist -- their
// systemd units and their containers -- and returns the names it cleaned up.
//
// The registry is the source of truth, as it is for port rules. A unit left
// behind by a deleted app is not inert: hostit-app@.service is Restart=always,
// so systemd retries it forever against a container that is gone, and its
// enable symlink starts it again after a reboot.
func (m *Manager) ReconcileOrphans() []string {
	out, err := m.runner.Run("systemctl", "list-units", unitTemplate+"*", "--all", "--no-legend", "--plain")
	if err != nil {
		slog.Warn("Cannot list app units to reconcile", "error", err)
		return nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps to reconcile units", "error", err)
		return nil
	}
	known := make(map[string]bool, len(apps))
	for _, a := range apps {
		known[a.Name] = true
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, ok := appNameFromUnit(strings.Fields(strings.TrimSpace(line)))
		if !ok || known[name] {
			continue
		}
		unit := unitName(name)
		if _, err := m.runner.Run("systemctl", "disable", "--now", unit); err != nil {
			slog.Warn("Cannot disable the unit of a deleted app", "app", name, "error", err)
		}
		// Without this the unit lingers in "failed" forever, which is how these
		// are noticed in the first place
		if _, err := m.runner.Run("systemctl", "reset-failed", unit); err != nil {
			slog.Debug("Cannot reset the unit of a deleted app", "app", name, "error", err)
		}
		removed = append(removed, name)
	}
	removed = append(removed, m.reconcileContainers(known)...)
	if len(removed) > 0 {
		slog.Info("Removed leftovers of deleted apps", "apps", removed)
	}
	return removed
}

// reconcileContainers removes containers whose app is gone. Deleting an app
// races the background start that follows creating one: if the start wins, it
// leaves a container behind that nothing will ever run.
func (m *Manager) reconcileContainers(known map[string]bool) []string {
	out, err := m.runner.Run("podman", "ps", "--all", "--format", "{{.Names}}")
	if err != nil {
		slog.Warn("Cannot list containers to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), containerPrefix)
		if !ok || name == "" || known[name] {
			continue
		}
		if _, err := m.runner.Run("podman", "rm", "--force", containerName(name)); err != nil {
			slog.Warn("Cannot remove the container of a deleted app", "app", name, "error", err)
			continue
		}
		removed = append(removed, name)
	}
	return removed
}

// appNameFromUnit picks the app name out of a "systemctl list-units" line
func appNameFromUnit(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	unit := strings.TrimSuffix(strings.TrimPrefix(fields[0], "\u25cf "), ".service")
	name, ok := strings.CutPrefix(unit, unitTemplate)
	if !ok || name == "" || strings.Contains(name, "@") {
		return "", false
	}
	return name, true
}

// RestartStaleAgents restarts every running app whose agent predates this build
// and returns the names it restarted.
//
// The agent is PID 1 in an app's container, exec'd from the hostit binary as it
// was at the time. The binary on disk is bind-mounted, so an upgrade replaces
// the file, but a running agent keeps the behaviour it started with -- and it
// is the agent that decides what the app's run command actually is. A static
// app once kept serving its old directory through an upgrade that way, with the
// app's whole home on the internet. Restarting costs each app a moment of
// downtime, so it only happens when the version actually changed.
func (m *Manager) RestartStaleAgents(version string) ([]string, error) {
	settings, err := m.store.Settings()
	if err != nil {
		return nil, err
	}
	if settings[settingAgentVersion] == version {
		return nil, nil
	}
	apps, err := m.store.Apps()
	if err != nil {
		return nil, err
	}
	restarted := make([]string, 0, len(apps))
	for _, a := range apps {
		// Up, not just a restart: a new binary may also want the container built
		// differently (different mounts, different arguments), and only apply
		// notices that
		if _, err := m.Up(a.Name); err != nil {
			slog.Warn("Cannot bring app up after upgrade", "app", a.Name, "error", err)
			continue
		}
		restarted = append(restarted, a.Name)
	}
	return restarted, m.store.SetSetting(settingAgentVersion, version)
}

// apply converges the app container to the desired config: recreate it when the
// configuration changed, start it, or (allowReload) signal the agent to restart
// just the run command
func (m *Manager) apply(a *store.App, conf *appctl.AppConfig, allowReload bool) (string, error) {
	name := a.Name
	ids, err := m.ops.LookupIDs(name)
	if err != nil {
		return "", err
	}
	// The daemon builds this at startup, but an app created while that is still
	// running must not fail; EnsureWorkspaceImage is a no-op once it exists
	if err := m.EnsureWorkspaceImage(); err != nil {
		return "", err
	}

	// Recreate the container if the desired config differs from the running one
	started := time.Now()
	desired := containerCreateArgs(conf, a, m.appHome(name), m.config.SocketFile, hostitBinFile, m.memoryLimit(name), ids)
	hash := containerConfigHash(desired)
	current, err := m.runner.Run("podman", "container", "inspect", containerName(name), "--format", inspectHashFormat)
	recreated := false
	if err != nil || strings.TrimSpace(current) != hash {
		// Creating an app starts it in the background, and the owner may delete it
		// while that is still in flight. Checking again here keeps the loser of
		// that race from leaving a container behind; ReconcileOrphans catches the
		// rest of the window at the next start.
		if _, err := m.store.App(name); err != nil {
			return "", err
		}
		_, _ = m.runner.Run("systemctl", "stop", unitName(name))
		_, _ = m.runner.Run("podman", "rm", "--force", containerName(name))
		createArgs := append([]string{"podman"}, withConfigLabel(desired, hash)...)
		if _, err := m.runner.Run(createArgs...); err != nil {
			return "", fmt.Errorf("cannot create container: %w", err)
		}
		recreated = true
		slog.Info("Container recreated", "app", name, "took", time.Since(started).Round(time.Second))
	}

	// Start if needed; otherwise a changed run: command only needs an agent reload
	if recreated || !m.isActive(name) {
		if _, err := m.runner.Run("systemctl", "enable", "--now", unitName(name)); err != nil {
			return "", err
		}
		if recreated {
			if _, err := m.runner.Run("systemctl", "restart", unitName(name)); err != nil {
				return "", err
			}
		}
		return "deployed (container created and started)", nil
	}
	if allowReload && conf != nil {
		if _, err := m.runner.Run("podman", "kill", "--signal", "HUP", containerName(name)); err != nil {
			return "", err
		}
		return "reloaded (agent restarted the run command)", nil
	}
	return "up to date", nil
}

func (m *Manager) isActive(name string) bool {
	out, err := m.runner.Run("systemctl", "is-active", unitName(name))
	return err == nil && strings.TrimSpace(out) == "active"
}

func (m *Manager) appHome(name string) string {
	return filepath.Join(m.config.AppsDir, name)
}

// loadConfig reads and validates an app's hostit.yml through its os.Root, so a
// symlink the tenant planted there cannot walk the root daemon out of the home,
// and the file is capped rather than read unbounded.
func (m *Manager) loadConfig(name string) (*appctl.AppConfig, error) {
	root, err := m.appRoot(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := readCapped(root, configFile, maxConfigSize)
	if err != nil {
		return nil, err
	}
	return appctl.LoadAppConfig(b)
}

// SetMemoryLimit records the container memory cap for an app; applied on the
// next container (re)creation
func (m *Manager) SetMemoryLimit(name string, memoryMB int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memoryMB[name] = memoryMB
}

// memoryLimit returns the recorded memory cap of an app
func (m *Manager) memoryLimit(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.memoryMB[name]
}

// withConfigLabel inserts the config-hash label into create args. A label is a
// flag, so it goes anywhere before the image; inserting it just ahead of the
// trailing image+command survives new flags being added at the front (the common
// change) rather than depending on a fixed offset into the leading flags.
func withConfigLabel(args []string, hash string) []string {
	cut := len(args) - containerArgsTrailer
	out := make([]string, 0, len(args)+2)
	out = append(out, args[:cut]...)
	out = append(out, "--label", "hostit.config="+hash)
	out = append(out, args[cut:]...)
	return out
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
