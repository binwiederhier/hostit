package app

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

// ReconcileOrphans removes the host state of apps that no longer exist -- their
// systemd units and their containers -- and returns the names it cleaned up.
//
// The registry is the source of truth, as it is for port rules. A unit left
// behind by a deleted app is not inert: hostit-app@.service is Restart=always,
// so systemd retries it forever against a container that is gone, and its
// enable symlink starts it again after a reboot.
func (m *Manager) ReconcileOrphans() []string {
	out, err := m.systemd.ListUnits(workspace.UnitTemplate + "*")
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
		if !ok || known[id] || m.containerForeign(id) {
			continue
		}
		unit := workspace.UnitName(id)
		if err := m.systemd.DisableNow(unit); err != nil {
			slog.Warn("Cannot disable the unit of a deleted app", "id", id, "error", err)
		}
		// Without this the unit lingers in "failed" forever, which is how these
		// are noticed in the first place
		if err := m.systemd.ResetFailed(unit); err != nil {
			slog.Debug("Cannot reset the unit of a deleted app", "id", id, "error", err)
		}
		removed = append(removed, id)
	}
	removed = append(removed, m.reconcileContainers(known)...)
	removed = append(removed, m.reconcileSubvolumes(known)...)
	m.reconcileBudgets(apps)
	if len(removed) > 0 {
		slog.Info("Removed leftovers of deleted apps", "apps", removed)
	}
	return removed
}

// reconcileBudgets destroys budget qgroups whose uid maps to no app: a destroy
// that stayed "Device or resource busy" during app delete (its member subvolume
// deletions had not committed yet) leaves the empty group behind. Budget groups
// are keyed "1/<uid>" and uids derive from ports, so the live set is computable
// from the registry alone.
func (m *Manager) reconcileBudgets(apps []*store.App) {
	groups, err := m.btrfs.ListBudgetGroups(m.config.AppsDir)
	if err != nil {
		slog.Debug("Cannot list budget qgroups to reconcile", "error", err)
		return
	}
	live := make(map[string]bool, len(apps))
	for _, a := range apps {
		live[budgetGroup(m.uidFor(a.Port))] = true
	}
	for _, group := range groups {
		if live[group] {
			continue
		}
		if err := m.btrfs.QgroupDestroy(m.config.AppsDir, group); err != nil {
			slog.Debug("Cannot destroy stray budget qgroup", "group", group, "error", err)
			continue
		}
		slog.Info("Removed a stray budget qgroup", "group", group)
	}
}

// reconcileSubvolumes removes app subvolumes left under AppsDir for apps no
// longer in the registry -- a delete that failed halfway, an app deleted while
// the daemon was down, or the root-owned stub userdel can leave behind (see
// DeleteApp). A live app's subvolume is never touched (an existing subvolume is
// never recreated, so deleting one by mistake would be data loss); the id-keyed
// known-set is the sole gate. Hidden entries (.bases, .snapshots, dotfiles) are
// never touched.
func (m *Manager) reconcileSubvolumes(known map[string]bool) []string {
	entries, err := os.ReadDir(m.config.AppsDir)
	if err != nil {
		slog.Warn("Cannot list app subvolumes to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, e := range entries {
		id := e.Name() // app subvolumes are named by app id
		if !e.IsDir() || strings.HasPrefix(id, ".") || known[id] {
			continue
		}
		path := filepath.Join(m.config.AppsDir, id)
		if err := m.btrfs.DeleteSubvolume(path); err != nil {
			slog.Debug("Cannot delete orphaned app subvolume; trying a plain remove", "id", id, "path", path, "error", err)
		}
		// The userdel stub is a plain (empty) directory that subvolume delete
		// refuses; remove it directly. A deleted app can also leave a file-less
		// tree behind (<id>/home/app, recreated by a state or file read racing
		// the delete): holding no files, clearing it cannot lose anything. A
		// tree with anything else in it is surfaced for a human rather than
		// deleted more aggressively.
		_ = os.Remove(path)
		if _, err := os.Stat(path); err == nil && filelessTree(path) {
			_ = os.RemoveAll(path)
		}
		if _, err := os.Stat(path); err == nil {
			slog.Warn("Orphaned app subvolume still present; leaving it in place", "id", id, "path", path)
			continue
		}
		slog.Info("Removed orphaned app subvolume", "id", id, "path", path)
		removed = append(removed, id)
	}
	return removed
}

// reconcileContainers removes containers whose app is gone. Deleting an app
// races the background start that follows creating one: if the start wins, it
// leaves a container behind that nothing will ever run.
func (m *Manager) reconcileContainers(known map[string]bool) []string {
	out, err := m.container.Names(true)
	if err != nil {
		slog.Warn("Cannot list containers to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, ok := strings.CutPrefix(strings.TrimSpace(line), workspace.ContainerPrefix)
		if !ok || id == "" || known[id] || m.containerForeign(id) {
			continue
		}
		if err := m.container.RemoveForce(workspace.ContainerName(id)); err != nil {
			slog.Warn("Cannot remove the container of a deleted app", "id", id, "error", err)
			continue
		}
		removed = append(removed, id)
	}
	return removed
}

// inspectMountsFormat lists a container's bind-mount sources, one per line.
const inspectMountsFormat = `{{range .Mounts}}{{.Source}}
{{end}}`

// containerForeign reports whether the id's container exists and belongs to
// ANOTHER node on this host: its apps-dir mount lies outside this node's
// pool. Colocated nodes share systemd and podman, and each node's mirror
// holds only its own rows -- without this check, each node's reconcile would
// tear down the other's apps as "orphans". No container (or an unreadable
// mount list) is not foreign, so genuinely dead leftovers still get cleaned.
func (m *Manager) containerForeign(id string) bool {
	out, err := m.container.Inspect(workspace.ContainerName(id), inspectMountsFormat)
	if err != nil || strings.TrimSpace(out) == "" {
		return false
	}
	for _, src := range strings.Fields(out) {
		if strings.HasPrefix(src, m.config.AppsDir+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

// idFromUnit picks the app id out of a "systemctl list-units" line (units are
// instantiated per app id)
func idFromUnit(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	unit := strings.TrimSuffix(strings.TrimPrefix(fields[0], "\u25cf "), ".service")
	id, ok := strings.CutPrefix(unit, workspace.UnitTemplate)
	if !ok || id == "" || strings.Contains(id, "@") {
		return "", false
	}
	return id, true
}

// filelessTree reports whether path holds directories and nothing else, so that
// removing it cannot lose data. Any file, symlink or unreadable entry makes it
// false, and the tree is then left for a human.
func filelessTree(path string) bool {
	fileless := true
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			fileless = false
			return filepath.SkipAll
		}
		return nil
	})
	return fileless
}
