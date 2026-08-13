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
