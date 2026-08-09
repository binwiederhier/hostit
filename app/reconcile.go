package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

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
	// Containers, units and home directories are all keyed on the app's id, so the
	// known-set holds ids and every reverse-parse below yields an id.
	known := make(map[string]bool, len(apps))
	for _, a := range apps {
		known[a.ID] = true
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, ok := idFromUnit(strings.Fields(strings.TrimSpace(line)))
		if !ok || known[id] {
			continue
		}
		unit := unitNameForID(id)
		if _, err := m.runner.Run("systemctl", "disable", "--now", unit); err != nil {
			slog.Warn("Cannot disable the unit of a deleted app", "id", id, "error", err)
		}
		// Without this the unit lingers in "failed" forever, which is how these
		// are noticed in the first place
		if _, err := m.runner.Run("systemctl", "reset-failed", unit); err != nil {
			slog.Debug("Cannot reset the unit of a deleted app", "id", id, "error", err)
		}
		removed = append(removed, id)
	}
	removed = append(removed, m.reconcileContainers(known)...)
	removed = append(removed, m.reconcileHomes(known)...)
	if len(removed) > 0 {
		slog.Info("Removed leftovers of deleted apps", "apps", removed)
	}
	return removed
}

// reconcileHomes sweeps empty home directories left under AppsDir for apps no
// longer in the registry -- e.g. the root-owned stub userdel can leave behind on
// btrfs (see DeleteApp). A non-empty orphan is logged but kept, so a surprise is
// surfaced for a human rather than silently deleted; hidden entries (.snapshots,
// .backup, dotfiles) are never touched.
func (m *Manager) reconcileHomes(known map[string]bool) []string {
	entries, err := os.ReadDir(m.config.AppsDir)
	if err != nil {
		slog.Warn("Cannot list app homes to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, e := range entries {
		id := e.Name() // home directories are named by app id
		if !e.IsDir() || strings.HasPrefix(id, ".") || known[id] {
			continue
		}
		home := filepath.Join(m.config.AppsDir, id)
		inner, err := os.ReadDir(home)
		if err != nil {
			slog.Warn("Cannot inspect orphaned app home", "id", id, "path", home, "error", err)
			continue
		}
		if len(inner) > 0 {
			slog.Warn("Orphaned app home is not empty; leaving it in place", "id", id, "path", home, "entries", len(inner))
			continue
		}
		if err := os.Remove(home); err != nil {
			slog.Warn("Cannot remove empty orphaned app home", "id", id, "path", home, "error", err)
			continue
		}
		slog.Info("Removed empty orphaned app home", "id", id, "path", home)
		removed = append(removed, id)
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
		id, ok := strings.CutPrefix(strings.TrimSpace(line), containerPrefix)
		if !ok || id == "" || known[id] {
			continue
		}
		if _, err := m.runner.Run("podman", "rm", "--force", containerNameForID(id)); err != nil {
			slog.Warn("Cannot remove the container of a deleted app", "id", id, "error", err)
			continue
		}
		removed = append(removed, id)
	}
	return removed
}

// idFromUnit picks the app id out of a "systemctl list-units" line (units are
// instantiated per app id)
func idFromUnit(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	unit := strings.TrimSuffix(strings.TrimPrefix(fields[0], "\u25cf "), ".service")
	id, ok := strings.CutPrefix(unit, unitTemplate)
	if !ok || id == "" || strings.Contains(id, "@") {
		return "", false
	}
	return id, true
}
