package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/workspace"
)

func TestEnsureWorkspaceImageBuildsOnce(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	// The Manager delegates to the workspace Service (which owns the build logic
	// and its mutex); this covers the wiring the daemon's startup path relies on.
	build := "podman build --tag " + workspace.ImageTag()
	require.NoError(t, m.EnsureWorkspaceImage())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
	// The image now exists, so a second call must not rebuild it
	require.NoError(t, m.EnsureWorkspaceImage())
	assert.Equal(t, 1, strings.Count(runner.ran(), build))
}

func TestPruneOldWorkspaceImages(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	current := workspace.ImageTag()
	runner.returns("podman images", current+"\nlocalhost/hostit-workspace:1\ndocker.io/library/debian:stable-slim\n")

	// Delegation to the workspace Service: old workspace tags go, the current
	// image and other people's images stay.
	m.PruneOldWorkspaceImages()
	ran := runner.ran()
	assert.Contains(t, ran, "podman rmi localhost/hostit-workspace:1")
	assert.NotContains(t, ran, "podman rmi "+current, "the image in use must survive")
	assert.NotContains(t, ran, "debian", "only hostit's own images are ours to remove")
}
