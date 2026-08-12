package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestEnsureWorkspaceImageBuildsOnce(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	build := "podman build --tag " + workspaceImageTag()
	require.NoError(t, m.EnsureWorkspaceImage())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
	// The build context holds the Containerfile the image is built from
	b, err := os.ReadFile(filepath.Join(m.config.DataDir, workspaceSubDir, "Containerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "FROM docker.io/library/debian")
	// The image now exists, so a second call must not rebuild it
	require.NoError(t, m.EnsureWorkspaceImage())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
}

func TestDeployReusesTheOneWorkspaceImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	runner.seedImage(workspaceImageTag()) // Built once, host-wide
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	runner.failOn("container inspect", assert.AnError) // Force a create
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := runner.ran()
	assert.NotContains(t, joined, "podman build", "the workspace image is shared, never rebuilt per app")
	assert.Contains(t, joined, "podman create --name "+m.containerName("blog"))
}

func TestContainerRunsUnderTheAppsOwnIdentity(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")
	runner.failOn("container inspect", assert.AnError)
	_, err := m.Up("blog")
	require.NoError(t, err)
	joined := runner.ran()
	// Container root maps to the app's unprivileged uid, so an escape lands
	// there rather than on real root, and its own network stack keeps it from
	// reaching other apps. One contiguous block, so podman idmap-mounts the image.
	uid := m.uidFor(10000)
	assert.Contains(t, joined, fmt.Sprintf("--uidmap 0:%d:65536", uid))
	assert.Contains(t, joined, fmt.Sprintf("--gidmap 0:%d:65536", uid))
	assert.Contains(t, joined, "--network slirp4netns")
	assert.Contains(t, joined, "--publish 127.0.0.1:10000:80")
}

func TestPruneOldWorkspaceImages(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	current := workspaceImageTag()
	runner.returns("podman images", current+"\nlocalhost/hostit-workspace:1\nlocalhost/hostit-workspace:deadbeef\ndocker.io/library/debian:stable-slim\n")

	// Every Containerfile change leaves its predecessor behind, and a workspace
	// image is well over a gigabyte on a 24 GB disk
	m.PruneOldWorkspaceImages()
	ran := runner.ran()
	assert.Contains(t, ran, "podman rmi localhost/hostit-workspace:1")
	assert.Contains(t, ran, "podman rmi localhost/hostit-workspace:deadbeef")
	assert.NotContains(t, ran, "podman rmi "+current, "the image in use must survive")
	assert.NotContains(t, ran, "debian", "only hostit's own images are ours to remove")
}

func TestEnsureAppImageSkipsBuildForPinnedExistingImage(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	// An app pinned to an image that already exists must NOT trigger a build, even
	// when the current workspace tag differs (a release changed the image). This is
	// what keeps the id-keying migration from blocking startup on an image build.
	runner.seedImage("localhost/hostit-workspace:oldtag")
	require.NoError(t, m.ensureAppImage(&store.App{Name: "blog", ImageTag: "localhost/hostit-workspace:oldtag"}))
	assert.NotContains(t, runner.ran(), "podman build", "a pinned, present image is not rebuilt")

	// An unpinned app builds the current image when it is missing.
	require.NoError(t, m.ensureAppImage(&store.App{Name: "shop"}))
	assert.Contains(t, runner.ran(), "podman build --tag "+workspaceImageTag())
}
