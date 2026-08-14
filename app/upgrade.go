package app

import (
	"log/slog"
)

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
		// A powered-off app stays off: an upgrade (and the storage migration
		// whose config-hash change precedes this) must not resurrect what an
		// operator deliberately disabled. Its container is simply recreated on
		// the next explicit power-on.
		if !m.systemd.IsEnabled(m.unitName(a.Name)) {
			continue
		}
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
