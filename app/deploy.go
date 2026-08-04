package app

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

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
	a, err := m.store.App(name)
	if err != nil {
		return "", err
	}
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if err := m.checkMountSources(name, conf); err != nil {
		return "", err
	}
	if conf.Build != "" {
		// One build at a time: podman serializes on its own lock anyway, but the
		// memory does not, and two Debian-sized builds OOM a 1 GB host
		m.buildMu.Lock()
		_, err := m.runner.Run("podman", "build", "--tag", buildImageTag(name), filepath.Join(m.appHome(name), conf.Build))
		m.buildMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("image build failed: %w", err)
		}
	}
	return m.apply(a, conf, true)
}

// Ensure makes sure the app's container exists and its service is running; it
// never recreates or reloads a live container (used on SSH login). Without a
// (valid) hostit.yml it provisions an idle workspace.
func (m *Manager) Ensure(name string) (string, error) {
	a, err := m.store.App(name)
	if err != nil {
		return "", err
	}
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
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
	if _, err := m.store.App(name); err != nil {
		return err
	}
	_, err := m.runner.Run("systemctl", "disable", "--now", unitName(name))
	return err
}

// Restart restarts the app's service (and thus its container)
func (m *Manager) Restart(name string) error {
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
	conf, err := appctl.LoadAppConfig(filepath.Join(m.appHome(name), "hostit.yml"))
	if err == nil && conf.Mode() == appctl.ModeContainer {
		// podman serializes on its own lock, and a build elsewhere can hold it for
		// minutes; a log fetch is a read path and must not wait that long
		return m.runner.RunTimeout(logsTimeout, "podman", "logs", "--tail", strconv.Itoa(lines), containerName(name))
	}
	// Through the app's root: .hostit/ lives in a directory the app user owns,
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

// checkMountSources resolves every path hostit is about to hand to root podman
// through the app's own root. Validation already refused absolute and climbing
// paths, but the app user can still plant a symlink: podman would follow it and
// mount whatever it points at.
func (m *Manager) checkMountSources(name string, conf *appctl.AppConfig) error {
	sources := make([]string, 0, len(conf.Volumes)+1)
	if conf.Build != "" {
		sources = append(sources, conf.Build)
	}
	for _, volume := range conf.Volumes {
		src, _, found := strings.Cut(volume, ":")
		if !found {
			return fmt.Errorf("%w: volume %q must be src:dst", ErrInvalid, volume)
		}
		sources = append(sources, src)
	}
	if len(sources) == 0 {
		return nil
	}
	root, err := m.appRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, src := range sources {
		if _, err := root.Stat(src); err != nil {
			return fmt.Errorf("%w: %q does not resolve inside the app directory: %s", ErrInvalid, src, err.Error())
		}
	}
	return nil
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
	if conf == nil || conf.Mode() != appctl.ModeContainer {
		if err := m.EnsureWorkspaceImage(); err != nil {
			return "", err
		}
	}

	// Recreate the container if the desired config differs from the running one
	desired := containerCreateArgs(conf, a, m.appHome(name), m.config.SocketFile, hostitBinFile, m.memoryLimit(name), ids)
	hash := containerConfigHash(desired)
	current, err := m.runner.Run("podman", "container", "inspect", containerName(name), "--format", inspectHashFormat)
	recreated := false
	if err != nil || strings.TrimSpace(current) != hash {
		_, _ = m.runner.Run("systemctl", "stop", unitName(name))
		_, _ = m.runner.Run("podman", "rm", "--force", containerName(name))
		createArgs := append([]string{"podman"}, withConfigLabel(desired, hash)...)
		if _, err := m.runner.Run(createArgs...); err != nil {
			return "", fmt.Errorf("cannot create container: %w", err)
		}
		recreated = true
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
	if allowReload && conf != nil && conf.Mode() != appctl.ModeContainer {
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

// withConfigLabel inserts the config-hash label into create args; options must
// precede the image, and args always start with "create --name X --hostname Y"
func withConfigLabel(args []string, hash string) []string {
	const optionsStart = 5 // len("create --name <name> --hostname <host>")
	out := make([]string, 0, len(args)+2)
	out = append(out, args[:optionsStart]...)
	out = append(out, "--label", "hostit.config="+hash)
	out = append(out, args[optionsStart:]...)
	return out
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
