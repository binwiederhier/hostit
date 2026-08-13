package app

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
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
	defer m.lockApp(name)()
	return m.up(name, true)
}

// up is Up without the per-app lock, for callers that already hold it (rollback).
// snapshot controls the pre-deploy safety snapshot: rollback passes false, having
// already taken its own safety snapshot of the pre-rollback state.
func (m *Manager) up(name string, snapshot bool) (string, error) {
	defer m.stateChanged(name)
	a, err := m.store.App(name)
	if err != nil {
		return "", err
	}
	conf, err := m.loadConfig(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	// Snapshot the current state before applying the new config, so a bad deploy is
	// undoable. Best effort: a snapshot failure must not block the deploy.
	if snapshot {
		m.snapshots.PreDeploySnapshot(name)
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
	// A deliberately powered-off app (its unit disabled) must not be resurrected by
	// a login: that would defeat poweroff, and the web terminal auto-reconnecting
	// after the drop would fight the operator. Only an explicit power-on (Up) clears
	// this. A crashed or fresh-reboot app is still enabled, so it starts as before.
	if !m.systemd.IsEnabled(m.unitName(name)) {
		return "", appctl.ErrPoweredOff
	}
	conf, err := m.loadConfig(name)
	if err != nil {
		conf = nil // Fall back to an idle workspace
	}
	if _, err := m.container.Inspect(m.containerName(name), inspectHashFormat); err == nil {
		if m.isActive(name) {
			return "workspace ready", nil
		}
		if err := m.systemd.EnableNow(m.unitName(name)); err != nil {
			return "", err
		}
		return "workspace started", nil
	}
	return m.apply(a, conf, false)
}

// RestartApp reloads the run: command inside the running container, without
// recreating the container -- the fast path for iterating on the app itself.
func (m *Manager) RestartApp(name string) error { return m.signalAgent(name, "HUP") }

// StopApp stops the run: command but leaves the container running, so a shell
// (SSH or the web terminal) and the container's state are untouched.
func (m *Manager) StopApp(name string) error { return m.signalAgent(name, "USR1") }

// StartApp starts the run: command again after StopApp
func (m *Manager) StartApp(name string) error { return m.signalAgent(name, "USR2") }

// signalAgent delivers a control signal to the app's agent (PID 1 in the
// container); it needs the container running, since it acts on the process.
func (m *Manager) signalAgent(name, signal string) error {
	defer m.stateChanged(name) // The app process just moved; drop the cache and re-measure
	if _, err := m.store.App(name); err != nil {
		return err
	}
	if err := m.container.Kill(m.containerName(name), signal); err != nil {
		return fmt.Errorf("%w: the container is not running (power it on first)", ErrInvalid)
	}
	return nil
}

// Down stops the app and disables it at boot
func (m *Manager) Down(name string) error {
	defer m.stateChanged(name)
	if _, err := m.store.App(name); err != nil {
		return err
	}
	return m.systemd.DisableNow(m.unitName(name))
}

// Restart restarts the app's service (and thus its container)
func (m *Manager) Restart(name string) error {
	defer m.stateChanged(name)
	if _, err := m.store.App(name); err != nil {
		return err
	}
	return m.systemd.Restart(m.unitName(name))
}

// Status returns the systemd status output for the app's service
func (m *Manager) Status(name string) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	out, err := m.systemd.Status(m.unitName(name))
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
	root, err := m.homefs.OpenRoot(m.appHome(name))
	if err != nil {
		return "", err
	}
	defer root.Close()
	b, err := m.homefs.ReadCapped(root, appLogFile, maxLogRead)
	if err != nil {
		return "", fmt.Errorf("no logs yet: %w", err)
	}
	return tailLines(string(b), lines), nil
}

// apply converges the app container to the desired config: recreate it when the
// configuration changed, start it, or (allowReload) signal the agent to restart
// just the run command
func (m *Manager) apply(a *store.App, conf *appctl.AppConfig, allowReload bool) (string, error) {
	name := a.Name
	ids, err := m.lookupIDs(name)
	if err != nil {
		return "", err
	}
	// Make sure the image THIS app uses is present -- its pinned tag, not
	// necessarily the current one. An app pinned to an image that already exists
	// needs no build, so recreating it (e.g. during the id-keying migration) never
	// blocks on building a new image; only an app that actually needs the current
	// image pays for building it.
	if err := m.workspace.EnsureAppImage(a); err != nil {
		return "", err
	}

	// Recreate the container if the desired config differs from the running one
	started := time.Now()
	desired := workspace.CreateArgs(conf, a, m.appHome(name), m.config.SocketFile, hostitBinFile, Version, m.memoryLimit(name), ids)
	hash := workspace.ConfigHash(desired)
	current, err := m.container.Inspect(m.containerName(name), inspectHashFormat)
	recreated := false
	if err != nil || strings.TrimSpace(current) != hash {
		// Creating an app starts it in the background, and the owner may delete it
		// while that is still in flight. Checking again here keeps the loser of
		// that race from leaving a container behind; ReconcileOrphans catches the
		// rest of the window at the next start.
		if _, err := m.store.App(name); err != nil {
			return "", err
		}
		_ = m.systemd.Stop(m.unitName(name))
		_ = m.container.RemoveForce(m.containerName(name))
		if err := m.container.Create(workspace.WithConfigLabel(desired, hash)...); err != nil {
			return "", fmt.Errorf("cannot create container: %w", err)
		}
		recreated = true
		slog.Info("Container recreated", "app", name, "took", time.Since(started).Round(time.Second))
	}

	// Start if needed; otherwise a changed run: command only needs an agent reload
	if recreated || !m.isActive(name) {
		if err := m.systemd.EnableNow(m.unitName(name)); err != nil {
			return "", err
		}
		if recreated {
			if err := m.systemd.Restart(m.unitName(name)); err != nil {
				return "", err
			}
		}
		return "deployed (container created and started)", nil
	}
	if allowReload && conf != nil {
		if err := m.container.Kill(m.containerName(name), "HUP"); err != nil {
			return "", err
		}
		return "reloaded (agent restarted the run command)", nil
	}
	return "up to date", nil
}

func (m *Manager) isActive(name string) bool {
	out, err := m.systemd.IsActive(0, m.unitName(name))
	return err == nil && strings.TrimSpace(out) == "active"
}

// appID resolves an app's stable id from its current name. The home directory and
// its snapshots are keyed on the id so a rename never moves them; callers still
// address apps by name and this is the single translation point. An unknown name
// falls back to itself, so a stray lookup fails on a missing path rather than here.
func (m *Manager) appID(name string) string {
	a, err := m.store.App(name)
	if err != nil {
		return name
	}
	return a.ID
}

// appHome is an app's home directory, keyed on its id (see appID).
func (m *Manager) appHome(name string) string {
	return m.appHomeByID(m.appID(name))
}

// appHomeByID builds a home path straight from an id, for the create path where
// the app is not yet in the store to resolve a name through.
func (m *Manager) appHomeByID(id string) string {
	return filepath.Join(m.config.AppsDir, id)
}

// loadConfig reads and validates an app's hostit.yml through its os.Root, so a
// symlink the tenant planted there cannot walk the root daemon out of the home,
// and the file is capped rather than read unbounded.
func (m *Manager) loadConfig(name string) (*appctl.AppConfig, error) {
	root, err := m.homefs.OpenRoot(m.appHome(name))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := m.homefs.ReadCapped(root, configFile, maxConfigSize)
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

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
