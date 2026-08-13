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
	r.returns("inspect-internal rootid", "257\n")
	// Two pre-rootfs apps, one from before image pinning (empty tag), one with a
	// snapshot the owner decided not to keep across the migration.
	require.NoError(t, m.store.AddApp(&store.App{Name: "old", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10001, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	require.NoError(t, os.MkdirAll(m.appHome("old"), 0o755))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
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

	// Every app gets its rootfs (a snapshot of its base) and its budget group.
	for _, name := range []string{"old", "blog"} {
		a, err := m.store.App(name)
		require.NoError(t, err)
		assert.Contains(t, ran, "btrfs subvolume snapshot "+m.workspace.BasePath(workspace.ImageTag())+" "+m.workspace.RootfsPath(a.ID))
		group := testBudgetGroup(t, m, name)
		assert.Contains(t, ran, "btrfs qgroup create "+group+" "+m.config.AppsDir)
		assert.Contains(t, ran, fmt.Sprintf("btrfs qgroup limit -e %dM %s %s", defaultDiskCapMB, group, m.config.AppsDir))
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
	r.returns("inspect-internal rootid", "257\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, ImageTag: workspace.ImageTag()}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	a, err := m.store.App("blog")
	require.NoError(t, err)
	// The app already has its rootfs (e.g. the previous run was killed halfway):
	// the invariant says it is never recreated, so the resumed migration only
	// re-ensures the budget around it.
	require.NoError(t, os.MkdirAll(m.workspace.RootfsPath(a.ID), 0o700))
	r.reset()

	require.NoError(t, m.MigrateRootfsStorage("v0.9.0"))
	assert.False(t, strings.Contains(r.ran(), "btrfs subvolume snapshot"), "an existing rootfs is never recreated")
	assert.Contains(t, r.ran(), "btrfs qgroup create "+testBudgetGroup(t, m, a.Name)+" "+m.config.AppsDir)
}

// testBudgetGroup resolves an app's budget qgroup id the way the Manager does:
// from its (possibly fake) unix uid, not from its port.
func testBudgetGroup(t *testing.T, m *Manager, name string) string {
	t.Helper()
	ids, err := m.lookupIDs(name)
	require.NoError(t, err)
	return fmt.Sprintf("1/%d", ids.UID)
}
