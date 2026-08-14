package app

import (
	"fmt"
	"os"
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
	m, _, r := newTestDeployManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	// The app subvolume is already a full OS tree (/usr is the marker: homes
	// never contained one), so there is nothing left to stage or fold.
	require.NoError(t, os.MkdirAll(m.appSubvolume("blog")+"/usr", 0o755))
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot", "a unified app needs no legacy rootfs")
}
