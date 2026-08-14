package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An app deleted while the daemon was down leaves its subvolume behind;
// reconcile reaps it like orphan units and containers. A live app's subvolume
// is never touched (the never-recreated invariant would make that data loss),
// and neither are hidden entries (.bases, .snapshots, dotfiles).
func TestReconcileOrphansRemovesOrphanSubvolume(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "x", []byte("y"), 0)) // a live app: its subvolume exists on disk
	orphan := filepath.Join(m.config.AppsDir, "ghostid")
	require.NoError(t, os.MkdirAll(orphan, 0o700))
	hidden := filepath.Join(m.config.AppsDir, ".snapshots")
	require.NoError(t, os.MkdirAll(hidden, 0o755))
	r.reset()

	removed := m.ReconcileOrphans()
	assert.Contains(t, removed, "ghostid")
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+orphan)
	assert.NoDirExists(t, orphan)
	assert.NotContains(t, r.ran(), "btrfs subvolume delete "+m.appSubvolume("blog"), "a live app's subvolume must be left alone")
	assert.DirExists(t, m.appSubvolume("blog"))
	assert.DirExists(t, hidden, "hidden entries are never touched")
}

// The userdel stub -- a root-owned plain directory left where the subvolume was
// -- is not a subvolume, so `btrfs subvolume delete` refuses it; the sweep
// removes it directly when empty and surfaces (rather than force-deletes) a
// path that still will not go away.
func TestReconcileOrphansRemovesEmptyStubKeepsStubborn(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	stub := filepath.Join(m.config.AppsDir, "stubid")
	require.NoError(t, os.MkdirAll(stub, 0o755))
	stubborn := filepath.Join(m.config.AppsDir, "stubbornid")
	require.NoError(t, os.MkdirAll(stubborn, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stubborn, "f"), []byte("d"), 0o644))
	// The fake btrfs "deletes" without touching disk, standing in for the real
	// tool refusing a plain directory; only the empty stub is then removable.
	r.reset()

	removed := m.ReconcileOrphans()
	assert.Contains(t, removed, "stubid")
	assert.NoDirExists(t, stub)
	assert.NotContains(t, removed, "stubbornid")
	assert.DirExists(t, stubborn, "a path that will not go away is surfaced, not force-deleted")
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
