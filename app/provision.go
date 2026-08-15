package app

import (
	"fmt"
	"log/slog"
	"os"
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

// provisionSpec is everything the node half needs to build an app on this
// machine, resolved by the control side.
type provisionSpec struct {
	ID       string   // Stable app id; subvolume and container are keyed on it
	Name     string   // Unix account name (today: the app name)
	Port     int      // Loopback port; the uid block derives from it
	SSHKeys  []string // Full authorized_keys set (request + profile keys)
	SeedPath string   // Fork seed subvolume; "" builds a fresh app with the skeleton
	URL      string   // The app's public URL, for the skeleton's welcome page
}

// provision builds the app on this machine: subvolume (fresh or fork seed),
// unix user, authorized keys, and -- for a fresh app -- the demo skeleton. On
// failure it rolls back its own partial work and the machine is clean again.
func (m *Manager) provision(spec *provisionSpec) error {
	forking := spec.SeedPath != ""
	// Create the app's one subvolume: a fork snapshots the seed subvolume (an
	// instant CoW copy of the source's whole OS tree, files included); a fresh
	// app snapshots its pinned tag's base and gets an empty files dir at home/app
	// that the skeleton then fills. The tree stays root-owned: the container
	// idmap-mounts it, so creation is a metadata snapshot and nothing more.
	subvol := m.appSubvolumeByID(spec.ID)
	if forking {
		if err := m.workspace.ForkAppSubvolume(spec.SeedPath, spec.ID); err != nil {
			return fmt.Errorf("cannot seed %s: %w", spec.Name, err)
		}
	} else {
		if err := m.workspace.EnsureAppSubvolume(&store.App{ID: spec.ID, ImageTag: workspace.ImageTag()}); err != nil {
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
	return nil
}

// provisionRollback undoes provision after a later step failed: the user and
// the id-keyed subvolume go away. The app is not in the store on these early
// failures, so this deletes the concrete path rather than resolving it by
// name; a brand-new app has no snapshots to clean up.
func (m *Manager) provisionRollback(spec *provisionSpec) {
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
		if _, err := m.Up(name); err != nil {
			slog.Warn("Cannot start app; it exists but serves nothing yet",
				"app", name, "took", time.Since(started).Round(time.Second), "error", err)
			return
		}
		slog.Info("App started", "app", name, "forked", forking, "took", time.Since(started).Round(time.Second))
	}()
}

// deprovisionSpec is everything the teardown needs, captured by the control
// side BEFORE the registry rows are gone: once they are, name-keyed lookups
// (paths, ids, snapshots) resolve nothing.
type deprovisionSpec struct {
	Name      string
	Port      int
	UID       int // The budget qgroup key; UIDKnown guards a failed lookup
	UIDKnown  bool
	Unit      string
	Container string
	Subvol    string
	SnapsRoot string
	SnapPaths []string
}

// deprovision tears the app down on this machine; it runs in the background
// (the caller holds the app lock and the port/name reservations until done).
func (m *Manager) deprovision(spec *deprovisionSpec) {
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
	// rm -rf cannot remove, so delete them first.
	for _, path := range spec.SnapPaths {
		_ = m.btrfs.DeleteSubvolume(path)
	}
	_ = os.RemoveAll(spec.SnapsRoot)
	_ = m.btrfs.DeleteSubvolume(spec.Subvol)
	if err := m.user.Delete(spec.Name); err != nil {
		slog.Warn("Cannot delete the app's unix user; a later create at this uid cleans up", "app", spec.Name, "error", err)
	}
	// userdel --remove will not delete a home directory it does not own -- the
	// subvolume holding it was removed above, and any recreated stub is
	// root-owned -- so remove whatever is left, or an empty stub is orphaned
	// under AppsDir.
	if err := os.RemoveAll(spec.Subvol); err != nil {
		slog.Warn("Could not remove leftover app directory after deleting app", "app", spec.Name, "path", spec.Subvol, "error", err)
	}
	// The name is reusable the moment the unix user is gone: release it
	// BEFORE the qgroup polling below, which can wait a while for the
	// deleted subvolumes to commit -- a same-name recreate must not.
	m.mu.Lock()
	delete(m.tearingDown, spec.Name)
	m.mu.Unlock()
	// Drop the (now empty) budget qgroup -- gently: the full ladder's
	// filesystem sync stalls every concurrent btrfs operation on the pool
	// (a create's snapshot waited ~12s behind it), so the teardown polls a
	// plain destroy and leaves stragglers to the startup reconcile.
	if spec.UIDKnown {
		m.destroyBudgetGently(spec.UID)
	}
	slog.Info("App teardown complete", "app", spec.Name)
}
