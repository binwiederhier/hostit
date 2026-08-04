package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureWorkspaceImageBuildsOnce(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	require.NoError(t, m.EnsureWorkspaceImage())
	require.Len(t, ops.builds, 1)
	assert.Equal(t, workspaceImage, ops.builds[0].tag)
	b, err := os.ReadFile(filepath.Join(ops.builds[0].contextDir, "Containerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "FROM docker.io/library/debian")
	// The image now exists, so a second call must not rebuild it
	require.NoError(t, m.EnsureWorkspaceImage())
	assert.Len(t, ops.builds, 1)
}

func TestDeployReusesTheOneWorkspaceImage(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	ops.images[workspaceImage] = true // Built once, host-wide
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	runner.errs["container inspect"] = assert.AnError // Force a create
	_, err := m.Up("blog")
	require.NoError(t, err)
	assert.Empty(t, ops.builds, "the workspace image is shared, never rebuilt per app")
	joined := strings.Join(runner.commands, "\n")
	assert.Contains(t, joined, "podman create --name hostit-app-blog")
}

func TestContainerRunsUnderTheAppsOwnIdentity(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "run: ./server")
	runner.errs["container inspect"] = assert.AnError
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := strings.Join(runner.commands, "\n")
	// Container root maps to the app's unprivileged uid, so an escape lands
	// there rather than on real root, and its own network stack keeps it from
	// reaching other apps
	assert.Contains(t, joined, "--uidmap 0:1001:1")
	assert.Contains(t, joined, "--gidmap 0:1001:1")
	assert.Contains(t, joined, "--network slirp4netns")
	assert.Contains(t, joined, "--publish 127.0.0.1:10000:10000")
}
