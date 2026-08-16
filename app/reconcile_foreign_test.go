package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two colocated nodes share systemd and podman: reconcile must not treat the
// OTHER node's apps (unknown ids, since each mirror holds only its own rows)
// as orphans. A container whose apps-dir mount lies outside this node's pool
// is foreign; its unit and container stay untouched.
func TestReconcileSkipsForeignNodeContainers(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("systemctl list-units", "hostit-app@foreign1.service loaded active running\n")
	r.returns("podman ps --all", "hostit-app-foreign1\n")
	r.returns("podman container inspect hostit-app-foreign1", "/some/other/pool/foreign1\n")

	removed := m.ReconcileOrphans()
	assert.Empty(t, removed)
	assert.NotContains(t, r.ran(), "podman rm --force hostit-app-foreign1")
	assert.NotContains(t, r.ran(), "disable --now hostit-app@foreign1")
}

func TestReconcileRemovesOwnOrphans(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("systemctl list-units", "hostit-app@dead1.service loaded failed failed\n")
	r.returns("podman ps --all", "hostit-app-dead1\n")
	r.returns("podman container inspect hostit-app-dead1", m.config.AppsDir+"/dead1\n")

	removed := m.ReconcileOrphans()
	require.NotEmpty(t, removed)
	assert.Contains(t, r.ran(), "podman rm --force hostit-app-dead1")
}
