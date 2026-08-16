package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

// This file is the node-local half of app creation and deletion: everything
// that touches THIS machine (subvolumes, unix users, key files, skeletons,
// containers, qgroups). The registry bookkeeping stays in create()/DeleteApp()
// on the control side. The split is the NodeAgent seam of
// plans/260815-hostit-nodeagent.md: specs carry everything the node half
// needs, so it never reads the registry.

// ProvisionSpec is everything the node half needs to build an app on this
// machine, resolved by the control side.
type ProvisionSpec struct {
	// Host is the target node id; the control plane's routing agent sends the
	// spec there (the row does not exist yet when provisioning starts).
	Host    string   `json:"host"`
	ID      string   `json:"id"`       // Stable app id; subvolume and container are keyed on it
	Name    string   `json:"name"`     // Unix account name (today: the app name)
	Port    int      `json:"port"`     // Loopback port; the uid block derives from it
	SSHKeys []string `json:"ssh_keys"` // Full authorized_keys set (request + profile keys)
	// SeedAppID/SeedSnapshotID name the fork seed (the source app's subvolume,
	// or one of its snapshots); empty SeedAppID builds a fresh app with the
	// skeleton. IDs, not paths: the NODE resolves them against its own pool --
	// a control-computed absolute path is wrong on any node whose apps-dir is
	// not control's.
	SeedAppID      string `json:"seed_app_id"`
	SeedSnapshotID string `json:"seed_snapshot_id"`
	URL            string `json:"url"`     // The app's public URL, for the skeleton's welcome page
	DiskMB         int    `json:"disk_mb"` // Resolved disk cap; the budget qgroup is created and capped BEFORE the subvolume
}

// Provision builds the app on this machine: subvolume (fresh or fork seed),
// unix user, authorized keys, and -- for a fresh app -- the demo skeleton. On
// failure it rolls back its own partial work and the machine is clean again.
func (m *Manager) Provision(spec *ProvisionSpec) error {
	forking := spec.SeedAppID != ""
	// The budget qgroup exists and is capped BEFORE the subvolume, which is then
	// snapshotted INTO it (-i): membership is atomic at creation, so the cap
	// enforces from the app's first byte. A later "qgroup assign" would leave
	// the group unenforced until a quota rescan completes.
	group := budgetGroup(m.uidFor(spec.Port))
	_ = m.btrfs.QgroupCreate(m.config.AppsDir, group)
	if err := m.btrfs.QgroupLimitExclusive(m.config.AppsDir, group, effectiveDiskCapMB(spec.DiskMB)); err != nil {
		slog.Warn("Cannot cap the new app's disk budget", "app", spec.Name, "error", err)
	}
	// Create the app's one subvolume: a fork snapshots the seed subvolume (an
	// instant CoW copy of the source's whole OS tree, files included); a fresh
	// app snapshots its pinned tag's base and gets an empty files dir at home/app
	// that the skeleton then fills. The tree stays root-owned: the container
	// idmap-mounts it, so creation is a metadata snapshot and nothing more.
	subvol := m.appSubvolumeByID(spec.ID)
	if forking {
		seedPath := m.appSubvolumeByID(spec.SeedAppID)
		if spec.SeedSnapshotID != "" {
			seedPath = filepath.Join(m.config.AppsDir, snapshotsDirName, spec.SeedAppID, spec.SeedSnapshotID)
		}
		if err := m.workspace.ForkAppSubvolume(seedPath, spec.ID, group); err != nil {
			return fmt.Errorf("cannot seed %s: %w", spec.Name, err)
		}
	} else {
		if err := m.workspace.EnsureAppSubvolume(&store.App{ID: spec.ID, ImageTag: workspace.ImageTag()}, group); err != nil {
			return fmt.Errorf("cannot create app subvolume for %s: %w", spec.Name, err)
		}
	}
	slog.Info("Creating app", "app", spec.Name, "port", spec.Port, "forked", forking)
	// The Unix account's home is the files dir INSIDE the subvolume, so
	// scp/sftp/rsync land on the app's files; useradd's own mkdir is a no-op.
	files := m.appFilesByID(spec.ID)
	if err := m.user.Create(spec.Name, files.Path(), m.uidFor(spec.Port)); err != nil {
		_ = m.btrfs.DeleteSubvolume(subvol)
		return fmt.Errorf("cannot create user %s: %w", spec.Name, err)
	}
	if err := m.writeKeysIn(files, spec.Name, spec.SSHKeys); err != nil {
		m.provisionRollback(spec)
		return fmt.Errorf("cannot write authorized keys for %s: %w", spec.Name, err)
	}
	// A fork keeps the source's files; only a fresh app gets the demo skeleton.
	if !forking {
		if err := m.user.WriteSkeleton(files.Path(), skeletonFiles(spec.Name, spec.URL, workspace.Runtimes)); err != nil {
			m.provisionRollback(spec)
			return fmt.Errorf("cannot write skeleton for %s: %w", spec.Name, err)
		}
	}
	// Apply the node's port rules HERE, on the node: the app's unix user (whose
	// uid the rule keys on) lives on this machine, and the firewall table is
	// this node's -- neither is reachable from control in a split deployment.
	m.ReconcilePortRules()
	return nil
}

// provisionRollback undoes provision after a later step failed: the user and
// the id-keyed subvolume go away. The app is not in the store on these early
// failures, so this deletes the concrete path rather than resolving it by
// name; a brand-new app has no snapshots to clean up.
func (m *Manager) provisionRollback(spec *ProvisionSpec) {
	_ = m.user.Delete(spec.Name)
	_ = m.btrfs.DeleteSubvolume(m.appSubvolumeByID(spec.ID))
}

// startInBackground brings the app up without the API call waiting for a
// container (and, on the app user's first app, an image build) to come up.
func (m *Manager) startInBackground(name string, forking bool) {
	m.background.Add(1)
	go func() {
		defer m.background.Done()
		// How long this took is the question asked whenever an app "would not
		// start": the API returns at once, and the wait is podman's queue behind
		// whatever else the host is doing
		started := time.Now()
		if _, err := m.node.Up(name); err != nil {
			slog.Warn("Cannot start app; it exists but serves nothing yet",
				"app", name, "took", time.Since(started).Round(time.Second), "error", err)
			return
		}
		slog.Info("App started", "app", name, "forked", forking, "took", time.Since(started).Round(time.Second))
	}()
}

// DeprovisionSpec is everything the teardown needs, captured by the control
// side BEFORE the registry rows are gone: once they are, name-keyed lookups
// (paths, ids, snapshots) resolve nothing.
type DeprovisionSpec struct {
	// Host is the node the app lives on, captured before the row is removed.
	Host string `json:"host"`
	// ID keys everything on-disk (subvolume, snapshots dir); the node resolves
	// the paths against its own pool.
	ID        string `json:"id"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	UID       int    `json:"uid"` // The budget qgroup key; UIDKnown guards a failed lookup
	UIDKnown  bool   `json:"uid_known"`
	Unit      string `json:"unit"`
	Container string `json:"container"`
}

// Deprovision tears the app down on this machine; it runs in the background
// (the caller holds the app lock and the port/name reservations until done).
func (m *Manager) Deprovision(spec *DeprovisionSpec) {
	// Stop the app first: a running container keeps processes alive, and
	// userdel refuses to remove a user that still has any.
	if err := m.systemd.DisableNow(spec.Unit); err != nil {
		slog.Warn("Cannot disable the app's unit; reconciling at next start", "app", spec.Name, "error", err)
	}
	// The unit lingers in "failed" otherwise, and a Restart=always unit that
	// systemd still knows about keeps retrying a container that is gone.
	_ = m.systemd.ResetFailed(spec.Unit)
	_ = m.container.RemoveForce(spec.Container)
	// The app subvolume and its snapshots are subvolumes that userdel's
	// rm -rf cannot remove, so delete them first. Snapshot ids come from THIS
	// pool's directory, not the spec: the mirror rows are already gone when
	// the teardown runs, and paths must be node-local anyway.
	subvol := m.appSubvolumeByID(spec.ID)
	snapsRoot := filepath.Join(m.config.AppsDir, snapshotsDirName, spec.ID)
	if entries, err := os.ReadDir(snapsRoot); err == nil {
		for _, e := range entries {
			_ = m.btrfs.DeleteSubvolume(filepath.Join(snapsRoot, e.Name()))
		}
	}
	_ = os.RemoveAll(snapsRoot)
	_ = m.btrfs.DeleteSubvolume(subvol)
	if err := m.user.Delete(spec.Name); err != nil {
		slog.Warn("Cannot delete the app's unix user; a later create at this uid cleans up", "app", spec.Name, "error", err)
	}
	// userdel --remove will not delete a home directory it does not own -- the
	// subvolume holding it was removed above, and any recreated stub is
	// root-owned -- so remove whatever is left, or an empty stub is orphaned
	// under AppsDir.
	if err := os.RemoveAll(subvol); err != nil {
		slog.Warn("Could not remove leftover app directory after deleting app", "app", spec.Name, "path", subvol, "error", err)
	}
	// The name is reusable the moment the unix user is gone: release it
	// BEFORE the qgroup polling below, which can wait a while for the
	// deleted subvolumes to commit -- a same-name recreate must not.
	m.mu.Lock()
	delete(m.tearingDown, spec.Name)
	m.mu.Unlock()
	// Re-assert this node's port rules now that the app's row is gone from the
	// mirror: its loopback drop rule is dropped along with it.
	m.ReconcilePortRules()
	// Drop the (now empty) budget qgroup -- gently: the full ladder's
	// filesystem sync stalls every concurrent btrfs operation on the pool
	// (a create's snapshot waited ~12s behind it), so the teardown polls a
	// plain destroy and leaves stragglers to the startup reconcile.
	if spec.UIDKnown {
		// In its own background: the gentle polling can take up to a minute, and
		// Deprovision's return is what releases the app's name for reuse.
		m.background.Add(1)
		go func() {
			defer m.background.Done()
			m.destroyBudgetGently(spec.UID)
		}()
	}
	slog.Info("App teardown complete", "app", spec.Name)
}
