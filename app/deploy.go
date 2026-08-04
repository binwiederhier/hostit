package app

import (
	"fmt"
	"os"
	"os/exec"
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
	// defaultPodman is the unit-file ExecStart fallback if podman is not in PATH
	defaultPodman = "/usr/bin/podman"
	// unitRelPath is the app unit location relative to the app home
	unitRelPath = ".config/systemd/user/" + unitName + ".service"
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
	if conf.Build != "" {
		buildDir := conf.Build
		if !filepath.IsAbs(buildDir) {
			buildDir = filepath.Join(m.appHome(name), buildDir)
		}
		if _, err := m.runner.RunAsUser(name, "podman", "build", "--tag", buildImageTag, buildDir); err != nil {
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
	if _, err := m.runner.RunAsUser(name, "podman", "container", "inspect", containerName, "--format", inspectHashFormat); err == nil {
		if m.isActive(name) {
			return "workspace ready", nil
		}
		if err := m.installUnit(a); err != nil {
			return "", err
		}
		if _, err := m.runner.RunAsUser(name, "systemctl", "--user", "restart", unitName); err != nil {
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
	_, err := m.runner.RunAsUser(name, "systemctl", "--user", "disable", "--now", unitName)
	return err
}

// Restart restarts the app's service (and thus its container)
func (m *Manager) Restart(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	_, err := m.runner.RunAsUser(name, "systemctl", "--user", "restart", unitName)
	return err
}

// Status returns the systemd status output for the app's service
func (m *Manager) Status(name string) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	out, err := m.runner.RunAsUser(name, "systemctl", "--user", "status", "--no-pager", unitName)
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
		return m.runner.RunAsUser(name, "podman", "logs", "--tail", strconv.Itoa(lines), containerName)
	}
	b, err := os.ReadFile(filepath.Join(m.appHome(name), ".hostit", "app.log"))
	if err != nil {
		return "", fmt.Errorf("no logs yet: %w", err)
	}
	return tailLines(string(b), lines), nil
}

// apply converges the app container to the desired config: build workspace image
// if needed, recreate the container on config changes, install and start the
// unit, or (allowReload) HUP the agent for run-command-only changes
func (m *Manager) apply(a *store.App, conf *appctl.AppConfig, allowReload bool) (string, error) {
	name := a.Name

	// Workspace-mode (and idle) containers need the workspace image
	if conf == nil || conf.Mode() == appctl.ModeProcess {
		if _, err := m.runner.RunAsUser(name, "podman", "image", "exists", workspaceImage); err != nil {
			if err := m.buildWorkspaceImage(name); err != nil {
				return "", err
			}
		}
	}

	// Recreate the container if the desired config differs from the running one
	desired := containerCreateArgs(conf, a, m.appHome(name), m.config.SocketFile, hostitBinFile, m.memoryMB[name])
	hash := containerConfigHash(desired)
	current, err := m.runner.RunAsUser(name, "podman", "container", "inspect", containerName, "--format", inspectHashFormat)
	recreated := false
	if err != nil || strings.TrimSpace(current) != hash {
		_, _ = m.runner.RunAsUser(name, "systemctl", "--user", "stop", unitName)
		_, _ = m.runner.RunAsUser(name, "podman", "rm", "--force", containerName)
		createArgs := append([]string{"podman"}, withConfigLabel(desired, hash)...)
		if _, err := m.runner.RunAsUser(name, createArgs...); err != nil {
			return "", fmt.Errorf("cannot create container: %w", err)
		}
		recreated = true
	}
	if err := m.installUnit(a); err != nil {
		return "", err
	}

	// Start if needed; otherwise a changed run: command only needs an agent reload
	if recreated || !m.isActive(name) {
		if _, err := m.runner.RunAsUser(name, "systemctl", "--user", "restart", unitName); err != nil {
			return "", err
		}
		return "deployed (container created and started)", nil
	}
	if allowReload && conf != nil && conf.Mode() == appctl.ModeProcess {
		if _, err := m.runner.RunAsUser(name, "podman", "kill", "--signal", "HUP", containerName); err != nil {
			return "", err
		}
		return "reloaded (agent restarted the run command)", nil
	}
	return "up to date", nil
}

// installUnit writes the app's systemd user unit and enables it
func (m *Manager) installUnit(a *store.App) error {
	unit := workspaceUnitFile(a.Name, m.podmanPath())
	if err := m.ops.WriteUserFile(a.Name, m.appHome(a.Name), unitRelPath, unit, 0o644); err != nil {
		return err
	}
	if _, err := m.runner.RunAsUser(a.Name, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := m.runner.RunAsUser(a.Name, "systemctl", "--user", "enable", unitName); err != nil {
		return err
	}
	return nil
}

// buildWorkspaceImage builds the default workspace image in the user's rootless
// storage, from a Containerfile staged in a world-readable spot
func (m *Manager) buildWorkspaceImage(name string) error {
	dir := filepath.Join(m.config.DataDir, "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(workspaceContainerfile), 0o644); err != nil {
		return err
	}
	if _, err := m.runner.RunAsUser(name, "podman", "build", "--tag", workspaceImage, dir); err != nil {
		return fmt.Errorf("cannot build workspace image: %w", err)
	}
	return nil
}

func (m *Manager) isActive(name string) bool {
	out, err := m.runner.RunAsUser(name, "systemctl", "--user", "is-active", unitName)
	return err == nil && strings.TrimSpace(out) == "active"
}

func (m *Manager) appHome(name string) string {
	return filepath.Join(m.config.AppsDir, name)
}

// podmanPath resolves the podman binary for unit files; user PATHs may vary but
// unit ExecStart lines need an absolute path
func (m *Manager) podmanPath() string {
	if m.podman == "" {
		if p, err := exec.LookPath("podman"); err == nil {
			m.podman = p
		} else {
			m.podman = defaultPodman
		}
	}
	return m.podman
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

// SetMemoryLimit records the container memory cap for an app; applied on the
// next container (re)creation
func (m *Manager) SetMemoryLimit(name string, memoryMB int) {
	m.memoryMB[name] = memoryMB
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
