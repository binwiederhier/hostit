package app

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An app deleted while the daemon was down leaves its rootfs subvolume behind;
// reconcile reaps it like orphan homes and containers. A live app's rootfs is
// never touched (the never-recreated invariant would make that data loss).
func TestReconcileOrphansRemovesOrphanRootfs(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	require.NoError(t, os.MkdirAll(m.workspace.RootfsPath("ghostid"), 0o700))
	r.reset()

	removed := m.ReconcileOrphans()
	assert.Contains(t, removed, "ghostid")
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+m.workspace.RootfsPath("ghostid"))
	assert.NotContains(t, r.ran(), "btrfs subvolume delete "+m.workspace.RootfsPath(a.ID), "a live app's rootfs must be left alone")
}

func TestReconcileOrphansSweepsStrayBudgetGroups(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog") // port 10000 -> uid 1000000
	_ = a
	// A destroy that stayed "busy" during app delete leaves the budget group
	// behind; the reconcile sweeps any 1/<uid> group whose uid maps to no app.
	runner.returns("btrfs qgroup show", "0/5 16384 16384\n1/1000000 100 100\n1/1065536 100 100\n")
	runner.reset()
	m.ReconcileOrphans()
	ran := runner.ran()
	assert.Contains(t, ran, "btrfs qgroup destroy 1/1065536", "the stray group (no app on that uid) is destroyed")
	assert.NotContains(t, ran, "btrfs qgroup destroy 1/1000000", "the live app's group must survive")
}
