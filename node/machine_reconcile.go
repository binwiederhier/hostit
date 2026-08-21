package node

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

// Reconcile converges this node to the state control asserts. Control calls it
// on every connect and on a timer, handing over the whole desired state, so
// the node never reasons from a snapshot it has been sitting on: it builds what
// is missing (an app whose account is gone -- a rebuilt node -- is provisioned
// again from the same spec that created it), corrects what drifted (keys,
// limits), re-asserts port rules, and tears down what the desired state does not
// list. Applying it twice changes nothing.
//
// A nil desired state falls back to converging against the pushed mirror, which is
// what an older control sends.
func (m *Machine) Reconcile(desired *nodeapi.DesiredState) []string {
	if desired != nil {
		m.applyDesired(desired)
	}
	removed := m.reconcileOrphans(desiredIDs(desired))
	m.ReconcilePortRules()
	return removed
}

// applyDesired makes each app in the desired state true on this machine. Failures
// are logged per app rather than aborting: one broken app must not stop the
// rest of the node from converging.
func (m *Machine) applyDesired(desired *nodeapi.DesiredState) {
	for _, app := range desired.Apps {
		// Under the app's lock, so an in-flight create finishes first and this
		// pass then sees the account it made rather than racing it.
		unlock := m.LockApp(app.Name)
		rebuilt := false
		if !m.UserExists(app.Name) {
			// The app should be here and is not: a rebuilt node, a botched
			// provision, an account swept by mistake. Build it from the same
			// spec that created it originally.
			spec := app.ProvisionSpec
			unlock() // Provision takes the lock itself
			if err := m.Provision(&spec); err != nil {
				slog.Warn("Cannot provision a missing app during reconcile", "app", app.Name, "error", err)
				continue
			}
			slog.Info("Provisioned an app that was missing on this node", "app", app.Name)
			unlock = func() {}
			rebuilt = true
		}
		// Keys and limits are control's to assert; re-writing them every pass
		// is how a change that happened while this node was away lands.
		if err := m.SetKeys(app.Name, app.SSHKeys, nil); err != nil {
			slog.Warn("Cannot apply app keys during reconcile", "app", app.Name, "error", err)
		}
		m.SetMemoryLimit(app.Name, app.MemoryMB)
		m.SetDiskLimit(app.Name, app.DiskMB)
		unlock()
		// Powered off is part of the desired state, not just a flag control
		// keeps: an app an operator stopped must come out of this pass stopped.
		// Provisioning starts what it builds, so a rebuild would otherwise
		// resurrect everything that was deliberately off -- and so would drift
		// on a node that missed the poweroff. Rebuilding it at all is
		// deliberate: the account and its subvolume should exist, so the app
		// can be restored and powered on later.
		if app.PoweredOff && (rebuilt || m.isActive(app.Name)) {
			if err := m.Down(app.Name); err != nil {
				slog.Warn("Cannot power off an app during reconcile", "app", app.Name, "error", err)
			} else {
				slog.Info("Powered off an app that control lists as off", "app", app.Name)
			}
		}
	}
}

// desiredIDs is the id set the desired state lists, or nil when there is
// none (the caller then falls back to the mirror).
func desiredIDs(desired *nodeapi.DesiredState) map[string]bool {
	if desired == nil {
		return nil
	}
	ids := make(map[string]bool, len(desired.Apps))
	for _, app := range desired.Apps {
		ids[app.ID] = true
	}
	return ids
}

// ReconcileOrphans removes the host state of apps that no longer exist -- their
// systemd units, containers, subvolumes, budget qgroups and unix accounts --
// and returns the names it cleaned up. Every removal needs two sightings (see
// confirmOrphan).
//
// The registry is the source of truth, as it is for port rules. A unit left
// behind by a deleted app is not inert: hostit-app@.service is Restart=always,
// so systemd retries it forever against a container that is gone, and its
// enable symlink starts it again after a reboot.
// confirmOrphan reports whether a resource has now been seen orphaned TWICE
// running, recording the sighting either way. Nothing destructive happens on a
// first sighting: a create provisions the account, subvolume, container and
// unit BEFORE control's registry push necessarily reaches this node, so an app
// mid-create is indistinguishable from a leftover in a single pass. Seen live
// on stage twice -- a new app's account, then another's subvolume, deleted
// seconds after creation, leaving apps that never served. A race resolves
// itself because the next mirror carries the app; a genuine leftover is still
// absent on the second pass and goes then.
func (m *Machine) confirmOrphan(key string) bool {
	m.orphanMu.Lock()
	defer m.orphanMu.Unlock()
	m.orphansThisPass[key] = true
	return m.orphansLastPass[key]
}

// startOrphanPass begins a sweep's bookkeeping; endOrphanPass makes this
// pass's sightings the ones the next pass compares against.
func (m *Machine) startOrphanPass() {
	m.orphanMu.Lock()
	m.orphansThisPass = make(map[string]bool)
	m.orphanMu.Unlock()
}

func (m *Machine) endOrphanPass() {
	m.orphanMu.Lock()
	m.orphansLastPass = m.orphansThisPass
	m.orphanMu.Unlock()
}

func (m *Machine) ReconcileOrphans() []string {
	return m.reconcileOrphans(nil)
}

// reconcileOrphans sweeps against the given id set, or against the mirror when
// it is nil. The desired state is the fresher truth: it was built for this
// pass, where the mirror is whatever last arrived.
func (m *Machine) reconcileOrphans(known map[string]bool) []string {
	m.startOrphanPass()
	defer m.endOrphanPass()
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
	if known == nil {
		known = make(map[string]bool, len(apps))
		for _, a := range apps {
			known[a.ID] = true
		}
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, ok := idFromUnit(strings.Fields(strings.TrimSpace(line)))
		if !ok || known[id] || m.containerForeign(id) || !m.confirmOrphan("unit:"+id) {
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
	removed = append(removed, m.reconcileUsers(known)...)
	m.reconcileBudgets(apps)
	if len(removed) > 0 {
		slog.Info("Removed leftovers of deleted apps", "apps", removed)
	}
	return removed
}

// reconcileUsers removes the Unix accounts of apps that no longer exist. An app
// deleted while this node was disconnected keeps its account (the routed
// Deprovision was dropped), and that account is not inert: its gid squats the
// uid block its old port maps to, so the next app allocated that port fails to
// create ("groupadd: GID already exists").
//
// Only accounts whose home lies under THIS node's pool are touched. Colocated
// nodes share one /etc/passwd, and another node's app accounts -- whose ids are
// absent from this node's mirror by design -- must never be swept.
func (m *Machine) reconcileUsers(known map[string]bool) []string {
	accounts, err := m.user.List()
	if err != nil {
		slog.Warn("Cannot list app accounts to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, a := range accounts {
		id, ok := m.idFromPoolHome(a.Home)
		if !ok || known[id] || !m.confirmOrphan("user:"+a.Name) {
			continue
		}
		// Kill first: userdel refuses while anything runs as the account, and a
		// leftover session or container process is exactly what may still be there.
		if err := m.user.KillProcesses(a.Name); err != nil {
			slog.Debug("Cannot kill processes of an orphaned account", "user", a.Name, "error", err)
		}
		if err := m.user.Delete(a.Name); err != nil {
			slog.Warn("Cannot delete the account of a deleted app", "user", a.Name, "error", err)
			continue
		}
		removed = append(removed, a.Name)
	}
	return removed
}

// idFromPoolHome extracts the app id from an account home, and reports whether
// that home is in this node's apps pool at all (either the real AppsDir or the
// raw bind the daemon's file I/O goes through, which is what useradd recorded).
func (m *Machine) idFromPoolHome(home string) (string, bool) {
	clean := filepath.Clean(home)
	for _, base := range []string{m.config.AppsDir, m.rawAppsDir} {
		if base == "" {
			continue
		}
		rest, ok := strings.CutPrefix(clean, filepath.Clean(base)+string(filepath.Separator))
		if !ok || rest == "" {
			continue
		}
		return workspace.IDFromHomeDir(clean), true
	}
	return "", false
}

// reconcileBudgets destroys budget qgroups whose uid maps to no app: a destroy
// that stayed "Device or resource busy" during app delete (its member subvolume
// deletions had not committed yet) leaves the empty group behind. Budget groups
// are keyed "1/<uid>" and uids derive from ports, so the live set is computable
// from the registry alone.
func (m *Machine) reconcileBudgets(apps []*store.App) {
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
func (m *Machine) reconcileSubvolumes(known map[string]bool) []string {
	entries, err := os.ReadDir(m.config.AppsDir)
	if err != nil {
		slog.Warn("Cannot list app subvolumes to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, e := range entries {
		id := e.Name() // app subvolumes are named by app id
		if !e.IsDir() || strings.HasPrefix(id, ".") || known[id] || !m.confirmOrphan("subvol:"+id) {
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
func (m *Machine) reconcileContainers(known map[string]bool) []string {
	out, err := m.container.Names(true)
	if err != nil {
		slog.Warn("Cannot list containers to reconcile", "error", err)
		return nil
	}
	removed := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, ok := strings.CutPrefix(strings.TrimSpace(line), workspace.ContainerPrefix)
		if !ok || id == "" || known[id] || m.containerForeign(id) || !m.confirmOrphan("container:"+id) {
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
const (
	inspectMountsFormat = `{{range .Mounts}}{{.Source}}
{{end}}`
)

// containerForeign reports whether the id's container exists and belongs to
// ANOTHER node on this host: its apps-dir mount lies outside this node's
// pool. Colocated nodes share systemd and podman, and each node's mirror
// holds only its own rows -- without this check, each node's reconcile would
// tear down the other's apps as "orphans". No container (or an unreadable
// mount list) is not foreign, so genuinely dead leftovers still get cleaned.
func (m *Machine) containerForeign(id string) bool {
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
