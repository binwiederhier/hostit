package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

func TestMigrateRootfsStorage(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	// Two pre-rootfs apps, one from before image pinning (empty tag), one with a
	// snapshot the owner decided not to keep across the migration.
	require.NoError(t, m.store.AddApp(&store.App{Name: "old", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10001, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	require.NoError(t, os.MkdirAll(m.appSubvolume("old"), 0o755))
	require.NoError(t, os.MkdirAll(m.appSubvolume("blog"), 0o755))
	snap, err := m.TakeSnapshot("blog", "pre-migration", false)
	require.NoError(t, err)
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	ran := r.ran()

	// The empty tag is backfilled to the current image before anything else keys
	// on it, exactly like the old CreateArgs fallback did.
	old, err := m.store.App("old")
	require.NoError(t, err)
	assert.Equal(t, workspace.ImageTag(), old.ImageTag)

	// Every app gets a rootfs at the LEGACY .rootfs/<id> location (a snapshot of
	// its base, chowned to its block): the unification migration that runs right
	// after folds the home into it and moves it onto the app subvolume.
	for _, name := range []string{"old", "blog"} {
		a, err := m.store.App(name)
		require.NoError(t, err)
		assert.Contains(t, ran, "btrfs subvolume snapshot "+m.workspace.BasePath(workspace.ImageTag())+" "+m.legacyRootfsPath(a.ID))
		ids, err := m.lookupIDs(name)
		require.NoError(t, err)
		assert.Contains(t, ran, fmt.Sprintf("chown -R %d:%d %s", ids.UID, ids.GID, m.legacyRootfsPath(a.ID)))
	}

	// Existing snapshots are dropped (owner's call: budgets stay predictable):
	// subvolume and store row both.
	assert.Contains(t, ran, "btrfs subvolume delete "+m.snapshotPath("blog", snap.ID))
	snaps, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	assert.Empty(t, snaps)

	// A second run is a no-op: the settings gate remembers the migration ran.
	r.reset()
	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.NotContains(t, r.ran(), "btrfs", "an already migrated host must not be touched again")
}

func TestMigrateRootfsStorageLeavesExistingRootfsAlone(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	require.NoError(t, os.MkdirAll(m.appSubvolume("blog"), 0o755))
	a, err := m.store.App("blog")
	require.NoError(t, err)
	// The app already has its legacy rootfs (e.g. the previous run was killed
	// halfway): the invariant says it is never recreated, so the resumed
	// migration leaves it alone.
	require.NoError(t, os.MkdirAll(m.legacyRootfsPath(a.ID), 0o700))
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.False(t, strings.Contains(r.ran(), "btrfs subvolume snapshot"), "an existing rootfs is never recreated")
}

func TestMigrateRootfsStorageSkipsAUnifiedApp(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	// The app is already unified: its passwd home (root-maintained, so a tenant
	// cannot fake it) points inside the subvolume. Nothing to stage or fold.
	require.NoError(t, ops.SetHome("blog", m.appFiles("blog").Path()))
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot", "a unified app needs no legacy rootfs")
}

func TestMigrateRootfsStorageIgnoresATenantPlantedUsrMarker(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	a, err := m.store.App("blog")
	require.NoError(t, err)
	// In the pre-unification layout the app's home WAS the subvolume root, and the
	// tenant (root in their own container) owns it: a hostile tenant can plant a
	// "usr" dir there before the operator upgrades. That must not read as
	// "already unified", or the migration would skip staging the rootfs and the
	// unification would then run against a home with no OS tree -- a bricked app.
	require.NoError(t, os.MkdirAll(filepath.Join(m.appSubvolume("blog"), "usr"), 0o755))
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+m.workspace.BasePath(workspace.ImageTag())+" "+m.legacyRootfsPath(a.ID))
}

// seedTwoSubvolApp lays out one pre-unification app on the real test fs: a home
// "subvolume" at <apps>/<id> holding files (dotfiles included) and a staged
// rootfs at .rootfs/<id> shaped like an OS tree.
func seedTwoSubvolApp(t *testing.T, m *Manager, name string, port int) *store.App {
	t.Helper()
	require.NoError(t, m.store.AddApp(&store.App{Name: name, Port: port, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	a, err := m.store.App(name)
	require.NoError(t, err)
	home := m.appSubvolume(name)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, "hostit.yml"), []byte("mode: static\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "data.txt"), []byte("precious"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte("ssh-ed25519 KEY"), 0o600))
	legacy := m.legacyRootfsPath(a.ID)
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "usr", "local"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "etc"), 0o755))
	return a
}

func TestMigrateUnifiedStorage(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	r.emulateSubvolDelete = true
	r.returns("inspect-internal rootid", "300\n")
	a := seedTwoSubvolApp(t, m, "blog", 10000)
	snap, err := m.TakeSnapshot("blog", "pre-unify", false)
	require.NoError(t, err)
	// The app is running, so it must be stopped for the move and brought back up.
	r.returns("is-active", "active")
	r.reset()

	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	ran := r.ran()
	subvol := m.appSubvolume("blog")

	// The app is now ONE subvolume: the OS tree with the home folded in at
	// home/app, dotfiles included, and the old home subvolume is gone.
	assert.DirExists(t, filepath.Join(subvol, "usr", "local"))
	assert.FileExists(t, filepath.Join(subvol, "home", "app", "hostit.yml"))
	b, err := os.ReadFile(filepath.Join(subvol, "home", "app", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "precious", string(b))
	assert.FileExists(t, filepath.Join(subvol, "home", "app", ".ssh", "authorized_keys"), "dotfiles must be copied too")
	assert.NoDirExists(t, m.legacyRootfsPath(a.ID))
	assert.NoDirExists(t, filepath.Join(m.config.AppsDir, legacyRootfsDirName), "the empty .rootfs staging dir is removed")

	// The copy was a same-pool reflink, the unit was stopped (NOT disabled: the
	// power state must survive) and the container removed around the swap.
	assert.Contains(t, ran, "cp -a --reflink=always "+m.appSubvolume("blog")+"/. "+m.legacyRootfsPath(a.ID)+"/home/app/")
	assert.Contains(t, ran, "systemctl stop "+m.unitName("blog"))
	assert.NotContains(t, ran, "systemctl disable")
	assert.Contains(t, ran, "podman rm --force "+m.containerName("blog"))

	// Old snapshots are home-shaped and incompatible with whole-app rollback:
	// purged, subvolume and row. (The Up at the end takes a fresh pre-deploy
	// snapshot of the now-unified subvolume, which is fine -- only the old one
	// must be gone.)
	assert.Contains(t, ran, "btrfs subvolume delete "+m.snapshotPath("blog", snap.ID))
	snaps, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	for _, s := range snaps {
		assert.NotEqual(t, snap.ID, s.ID, "the home-shaped snapshot must be purged")
	}

	// The account home moves inside the subvolume, and the budget re-joins the
	// moved subvolume (its rootid survives the rename; ensure-style anyway).
	assert.Equal(t, m.appFiles("blog").Path(), ops.homes["blog"])
	assert.Contains(t, ran, "btrfs qgroup assign 0/300 "+testBudgetGroup(t, m, "blog")+" "+m.config.AppsDir)

	// The previously RUNNING app is brought back up (the config-hash change
	// recreates its container against the unified subvolume).
	assert.Contains(t, ran, "podman create --name "+m.containerName("blog"))

	// A second run is a no-op: the settings gate remembers the migration ran.
	r.reset()
	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	assert.NotContains(t, r.ran(), "cp -a", "an already unified host must not be touched again")
}

func TestMigrateUnifiedStorageIgnoresATenantPlantedUsrMarker(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.emulateSubvolDelete = true
	r.returns("inspect-internal rootid", "300\n")
	a := seedTwoSubvolApp(t, m, "blog", 10000)
	// A tenant-planted "usr" in the old home must not pass for the unified
	// layout: the fold must still run, or the home data would never reach
	// home/app and the tenant-shaped subvolume would become the container rootfs.
	require.NoError(t, os.MkdirAll(filepath.Join(m.appSubvolume("blog"), "usr"), 0o755))
	r.reset()

	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	assert.Contains(t, r.ran(), "cp -a --reflink=always "+m.appSubvolume("blog")+"/. "+m.legacyRootfsPath(a.ID)+"/home/app/")
	assert.FileExists(t, filepath.Join(m.appSubvolume("blog"), "home", "app", "data.txt"))
}

func TestMigrateUnifiedStorageLeavesAPoweredOffAppOff(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.emulateSubvolDelete = true
	r.returns("inspect-internal rootid", "300\n")
	seedTwoSubvolApp(t, m, "blog", 10000)
	// No is-active stub: the unit is not running (a powered-off app).
	r.reset()

	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	ran := r.ran()
	assert.DirExists(t, filepath.Join(m.appSubvolume("blog"), "usr"))
	// The layout moved, but the app stays down: no container, no enable, no start.
	assert.NotContains(t, ran, "podman create")
	assert.NotContains(t, ran, "enable --now")
	assert.NotContains(t, ran, "systemctl restart")
}

func TestMigrateUnifiedStorageResumesAfterAHalfMove(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	r.emulateSubvolDelete = true
	r.returns("inspect-internal rootid", "300\n")
	// A previous run was killed after deleting the old home but before the move:
	// only the staged rootfs (home already folded in) exists.
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	a, err := m.store.App("blog")
	require.NoError(t, err)
	legacy := m.legacyRootfsPath(a.ID)
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "usr"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "home", "app"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "home", "app", "data.txt"), []byte("precious"), 0o644))
	r.reset()

	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	assert.NotContains(t, r.ran(), "cp -a", "nothing left to copy on resume")
	assert.FileExists(t, filepath.Join(m.appSubvolume("blog"), "home", "app", "data.txt"))
	assert.Equal(t, m.appFiles("blog").Path(), ops.homes["blog"])
}

func TestMigrateUnifiedStorageConvergesAnAlreadyUnifiedApp(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "300\n")
	// Killed between the move and the budget tail: the staged rootfs is consumed
	// and the passwd home already points inside the subvolume (it is set right
	// before the move, so this state reads as unified), but the budget never
	// joined. The tail must still converge -- without stopping or copying.
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	require.NoError(t, os.MkdirAll(filepath.Join(m.appSubvolume("blog"), "usr"), 0o755))
	require.NoError(t, ops.SetHome("blog", m.appFiles("blog").Path()))
	r.reset()

	require.NoError(t, m.MigrateUnifiedStorage("v0.9.1"))
	assert.NotContains(t, r.ran(), "cp -a")
	assert.NotContains(t, r.ran(), "systemctl stop")
	assert.Equal(t, m.appFiles("blog").Path(), ops.homes["blog"])
	assert.Contains(t, r.ran(), "btrfs qgroup assign 0/300 "+testBudgetGroup(t, m, "blog")+" "+m.config.AppsDir)
}

// testBudgetGroup resolves an app's budget qgroup id the way the Manager does:
// from its (possibly fake) unix uid, not from its port.
func testBudgetGroup(t *testing.T, m *Manager, name string) string {
	t.Helper()
	ids, err := m.lookupIDs(name)
	require.NoError(t, err)
	return fmt.Sprintf("1/%d", ids.UID)
}
