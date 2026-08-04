package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageConf(t *testing.T) {
	t.Parallel()
	conf := storageConf("/var/lib/hostit/imagestore")
	assert.Contains(t, conf, `driver = "overlay"`)
	assert.Contains(t, conf, `additionalimagestores = ["/var/lib/hostit/imagestore"]`)
}

func TestCreateAppWritesStorageConf(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	conf, ok := ops.userFiles["blog:.config/containers/storage.conf"]
	require.True(t, ok, "storage.conf must be written so the shared image store is used")
	assert.Contains(t, conf, m.imageStoreDir())
}

func TestEnsureSharedImageBuildsOnce(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	require.NoError(t, m.EnsureSharedImage())
	require.Len(t, ops.sharedBuilds, 1)
	assert.Equal(t, m.imageStoreDir(), ops.sharedBuilds[0].storeDir)
	assert.Equal(t, workspaceImage, ops.sharedBuilds[0].tag)
	// The Containerfile is staged where the build can read it
	b, err := os.ReadFile(filepath.Join(ops.sharedBuilds[0].contextDir, "Containerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "FROM docker.io/library/debian")
	// A second call is a no-op: the image is already in the shared store
	ops.sharedImages[workspaceImage] = true
	require.NoError(t, m.EnsureSharedImage())
	assert.Len(t, ops.sharedBuilds, 1)
}

func TestDeployUsesSharedImageWithoutBuilding(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	ops.sharedImages[workspaceImage] = true
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	runner.errs["container inspect"] = assert.AnError // Force a create
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := strings.Join(runner.commands, "\n")
	assert.NotContains(t, joined, "podman build", "the shared image must not be rebuilt per user")
	assert.Contains(t, joined, "podman create --name hostit-app")
}
