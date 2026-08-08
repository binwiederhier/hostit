package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A deleted app must not leave its home directory behind. On btrfs, userdel
// --remove refuses to delete a home it does not own (the root-owned stub left
// after the subvolume is removed), which used to orphan an empty directory.
func TestDeleteAppRemovesHomeDirectory(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "index.html", []byte("hi"), 0))
	require.DirExists(t, m.appHome("blog"))

	require.NoError(t, m.DeleteApp("blog"))
	assert.NoDirExists(t, m.appHome("blog"))
}

// Reconcile sweeps empty home directories left behind for apps no longer in the
// registry, but never touches a live app, a hidden dir, or a non-empty orphan
// (which is surfaced for a human rather than silently deleted).
func TestReconcileOrphansRemovesEmptyOrphanHome(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "x", []byte("y"), 0)) // a live app: home exists on disk

	orphan := filepath.Join(m.config.AppsDir, "ghost")
	require.NoError(t, os.MkdirAll(orphan, 0o755))
	hidden := filepath.Join(m.config.AppsDir, ".snapshots")
	require.NoError(t, os.MkdirAll(hidden, 0o755))
	nonEmpty := filepath.Join(m.config.AppsDir, "keepme")
	require.NoError(t, os.MkdirAll(nonEmpty, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "f"), []byte("d"), 0o644))

	m.ReconcileOrphans()

	assert.NoDirExists(t, orphan)          // empty orphan swept
	assert.DirExists(t, m.appHome("blog")) // live app kept
	assert.DirExists(t, hidden)            // hidden entry kept
	assert.DirExists(t, nonEmpty)          // non-empty orphan preserved, not deleted
}
