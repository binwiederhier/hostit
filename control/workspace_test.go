package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/workspace"
)

func TestEnsureWorkspaceBaseBuildsAndExportsOnce(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	// The Manager delegates to the workspace Service (which owns the build logic
	// and its mutex); this covers the wiring the daemon's startup path relies on.
	build := "podman build --tag " + workspace.ImageTag()
	require.NoError(t, m.testMachine().EnsureWorkspaceBase())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
	assert.Equal(t, 1, strings.Count(runner.ran(), "podman export"), "the image is exported into the base subvolume")
	// The base now exists, so a second call must neither rebuild nor re-export
	require.NoError(t, m.testMachine().EnsureWorkspaceBase())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
	assert.Equal(t, 1, strings.Count(runner.ran(), "podman export"))
}

func TestPruneOldWorkspaceImages(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	current := workspace.ImageTag()
	runner.returns("podman images", current+"\nlocalhost/hostit-workspace:1\ndocker.io/library/debian:stable-slim\n")

	// Delegation to the workspace Service: old workspace tags go, the current
	// image and other people's images stay.
	m.testMachine().PruneOldWorkspaceImages()
	ran := runner.ran()
	assert.Contains(t, ran, "podman rmi localhost/hostit-workspace:1")
	assert.NotContains(t, ran, "podman rmi "+current, "the image in use must survive")
	assert.NotContains(t, ran, "debian", "only hostit's own images are ours to remove")
}

func TestRawAppsViewRoutesFileIO(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.testMachine().WriteFile("blog", "before.txt", []byte("via apps dir"), 0))

	// While a container runs, podman's idmapped rootfs mount covers the app's
	// subvolume path in the HOST namespace, and root writing through that view
	// EOVERFLOWs (root is not in the mapping). The daemon therefore does all
	// file I/O through a raw bind of the apps dir that excludes those child
	// mounts; UseRawAppsView is what serve calls once the bind exists.
	raw := t.TempDir()
	id := m.testMachine().AppID("blog")
	require.NoError(t, os.MkdirAll(filepath.Join(raw, id, "home", "app"), 0o755))
	m.testMachine().UseRawAppsView(raw)
	require.NoError(t, m.testMachine().WriteFile("blog", "after.txt", []byte("via raw view"), 0))
	assert.FileExists(t, filepath.Join(raw, id, "home", "app", "after.txt"))

	// The subvolume/btrfs side is untouched: podman and snapshots keep the real
	// path (destructive ops stop the container first, clearing the overmount).
	assert.Equal(t, filepath.Join(m.config.AppsDir, id), m.testMachine().AppSubvolume("blog"))
}

func TestMountRawAppsViewBindsPrivate(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	raw := filepath.Join(t.TempDir(), "apps-raw")
	r.reset()
	require.NoError(t, m.testMachine().MountRawAppsView(raw))
	ran := r.ran()
	// The apps mount is shared by default, so overmounts created after a plain
	// bind would PROPAGATE into it (seen on stage: every container start put its
	// idmapped mount into the raw view too). The bind must be private -- and an
	// existing bind is torn down and rebuilt, made rprivate FIRST so the
	// unmounts cannot propagate back onto the running containers' mounts.
	assert.Contains(t, ran, "mount --make-rprivate "+raw)
	assert.Contains(t, ran, "umount -R "+raw)
	assert.Contains(t, ran, "mount --bind "+m.config.AppsDir+" "+raw)
	assert.Contains(t, ran, "mount --make-private "+raw)
}
