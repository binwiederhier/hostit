package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateToIDKeyedHomes proves the one-off migration moves a pre-id app's
// name-keyed home to its id-keyed path, repoints the Unix user, and recreates the
// container once so the new bind-mount source takes effect.
func TestMigrateToIDKeyedHomes(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")

	// Simulate an app created before id-keying: its home sits at apps/<name>.
	idHome := m.appHomeByID(a.ID)
	nameHome := filepath.Join(m.config.AppsDir, "blog")
	require.NoError(t, os.Rename(idHome, nameHome))
	require.NoError(t, os.WriteFile(filepath.Join(nameHome, "marker"), []byte("data"), 0o600))
	runner.reset()

	m.MigrateToIDKeyedHomes()

	// The home (with its contents) moved to the id-keyed path; the old path is gone.
	assert.DirExists(t, idHome)
	assert.NoDirExists(t, nameHome)
	assert.FileExists(t, filepath.Join(idHome, "marker"))
	// The Unix user was repointed at the moved home.
	assert.Equal(t, idHome, ops.userHomes["blog"])
	// The container was torn down and brought back once, keyed by id throughout.
	ran := runner.ran()
	assert.Contains(t, ran, "podman rm --force "+containerNameForID(a.ID))

	// Idempotent: a second run has nothing to move.
	runner.reset()
	m.MigrateToIDKeyedHomes()
	assert.NotContains(t, runner.ran(), "podman rm --force "+containerNameForID(a.ID))
}

// TestMigrateToIDKeyedHomesClearsEmptyStub covers the real-world case where podman
// auto-created an empty id-home stub (a container mounted it before the real home
// moved): the migration must clear the empty stub and still move the real data.
func TestMigrateToIDKeyedHomesClearsEmptyStub(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")

	idHome := m.appHomeByID(a.ID)
	nameHome := filepath.Join(m.config.AppsDir, "blog")
	require.NoError(t, os.Rename(idHome, nameHome))
	require.NoError(t, os.WriteFile(filepath.Join(nameHome, "marker"), []byte("data"), 0o600))
	// Recreate the id path as an empty root-owned stub, exactly what podman leaves.
	require.NoError(t, os.MkdirAll(idHome, 0o750))

	m.MigrateToIDKeyedHomes()

	assert.DirExists(t, idHome)
	assert.NoDirExists(t, nameHome)
	assert.FileExists(t, filepath.Join(idHome, "marker"), "the real data replaced the empty stub")
}
