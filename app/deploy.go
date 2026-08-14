package app

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/homefs"
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

// Ensure is the login path (SSH shell, browser terminal): it brings the app's
// container up so there is something to exec into, but refuses a deliberately
// powered-off one. A login must not resurrect a powered-off app -- that would
// defeat poweroff, and the web terminal auto-reconnecting after the drop would
// fight the operator. A crashed or fresh-reboot app is still enabled, so it starts
// as before; only an explicit PowerOn clears a poweroff.
func (m *Manager) Ensure(name string) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	if !m.systemd.IsEnabled(m.unitName(name)) {
		return "", appctl.ErrPoweredOff
	}
	return m.powerOn(name)
}

// PowerOn brings the app's container up, clearing a prior poweroff by re-enabling
// its unit. Unlike Ensure it never refuses a powered-off app -- powering it on is
// exactly the point.
func (m *Manager) PowerOn(name string) (string, error) {
	return m.powerOn(name)
}

// powerOn makes sure the app's container exists and its service is running; it
// never recreates or reloads a live container. Without a (valid) hostit.yml it
// provisions an idle workspace.
func (m *Manager) powerOn(name string) (string, error) {
	defer m.stateChanged(name)
	a, err := m.store.App(name)
	if err != nil {
		return "", err
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

// Logs returns the last lines of the app's output, from the agent's log file.
func (m *Manager) Logs(name string, lines int) (string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", err
	}
	// Through the app's root: log/ lives in a directory the app user owns,
	// so the log file can be a symlink to anything the daemon can read
	root, err := m.homefs.OpenRoot(m.appFiles(name))
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
	// Make sure the subvolume THIS app runs exists -- a snapshot of its pinned
	// tag's base, which is only built/exported when the app actually needs it. An
	// app whose subvolume already exists needs nothing here (the invariant: an
	// existing app subvolume is never recreated), so recreating its container
	// never blocks on an image build or export.
	if err := m.workspace.EnsureAppSubvolume(a, ids); err != nil {
		return "", err
	}

	// Recreate the container if the desired config differs from the running one
	started := time.Now()
	desired := workspace.CreateArgs(conf, a, m.appSubvolume(name), m.config.SocketFile, hostitBinFile, Version, m.memoryLimit(name), ids)
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

	// Start if needed; otherwise a changed run: command only needs an agent reload.
	// Exactly one start, never enable-then-restart: that pair started every fresh
	// container twice (run 1 died ~300ms in when the restart tore it down, and
	// Restart=always brought up run 2), a churn window that raced every early
	// stop/start/deploy against a container that was about to die. A recreated
	// container whose unit is still active (the Stop above failed, or something
	// raced it back up) DOES need the bounce -- EnableNow would be a no-op against
	// the unit still attached to the old container.
	if active := m.isActive(name); recreated || !active {
		if recreated && active {
			if err := m.systemd.Restart(m.unitName(name)); err != nil {
				return "", err
			}
		} else if err := m.systemd.EnableNow(m.unitName(name)); err != nil {
			return "", err
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

// appID resolves an app's stable id from its current name. The app subvolume and
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

// appSubvolume is the app's one subvolume (the container's whole OS tree, with
// the app's files at home/app inside it), keyed on its id (see appID).
func (m *Manager) appSubvolume(name string) string {
	return m.appSubvolumeByID(m.appID(name))
}

// appSubvolumeByID builds the subvolume path straight from an id, for the create
// path where the app is not yet in the store to resolve a name through. The
// workspace service owns the layout fact; this only saves callers the hop.
func (m *Manager) appSubvolumeByID(id string) string {
	return m.workspace.AppSubvolumePath(id)
}

// appFiles locates an app's files directory (home/app INSIDE the subvolume) for
// the homefs service, which resolves the tenant-owned inner path within the
// subvolume's os.Root rather than as one plain host path.
func (m *Manager) appFiles(name string) homefs.Dir {
	return m.appFilesByID(m.appID(name))
}

// appFilesByID is appFiles for the create path, where the app is not yet in the
// store to resolve a name through.
func (m *Manager) appFilesByID(id string) homefs.Dir {
	return homefs.Dir{Subvolume: m.appSubvolumeByID(id), Rel: workspace.FilesDir}
}

// loadConfig reads and validates an app's hostit.yml through its os.Root, so a
// symlink the tenant planted there cannot walk the root daemon out of the home,
// and the file is capped rather than read unbounded.
func (m *Manager) loadConfig(name string) (*appctl.AppConfig, error) {
	root, err := m.homefs.OpenRoot(m.appFiles(name))
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
