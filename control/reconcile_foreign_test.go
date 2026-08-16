package control

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

func TestProvisionAppliesPortRulesOnTheNode(t *testing.T) {
	t.Parallel()
	// Port rules are applied by the NODE half (Provision), so a remote node
	// firewalls its own apps -- control cannot (the unix user lives on the
	// node, and control's firewall table is a different one).
	m, ops, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")

	require.NotEmpty(t, ops.portRules, "provision must apply the node's port rules")
	last := ops.portRules[len(ops.portRules)-1]
	found := false
	for _, r := range last {
		if r.Port == a.Port {
			found = true
		}
	}
	assert.True(t, found, "the new app's port must be in the applied rules")
}

func TestReconcileAppliesPortRules(t *testing.T) {
	t.Parallel()
	// Reconcile (run on every rejoin) re-asserts the node's port rules along
	// with tearing down orphans: after a control outage the node's firewall
	// must converge to the synced mirror.
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	before := len(ops.portRules)
	m.Reconcile()
	assert.Greater(t, len(ops.portRules), before, "reconcile re-applies the port rules")
}
