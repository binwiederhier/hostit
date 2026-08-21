package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// The policy feeds the desired state control asserts on every rejoin, so it
// must honor per-app overrides: otherwise a node restart quietly reverts an
// admin's cap to the owner's defaults (found live on stage 2026-08-20 -- the
// PATCH applied, the next daemon restart un-applied it).
func TestPolicyLimitsHonorPerAppOverrides(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().UpdateAppLimits("blog", 768, 4096, 1500))

	policy := &serverPolicy{s: s}
	overridden, err := s.apps.Store().App("blog")
	require.NoError(t, err)
	mem, disk := policy.Limits(overridden)
	assert.Equal(t, 768, mem, "the admin's override outranks the owner's default")
	assert.Equal(t, 4096, disk)

	inherited, err := s.apps.Store().App("wiki")
	require.NoError(t, err)
	defMem, defDisk := policy.Limits(inherited)
	assert.NotEqual(t, 768, defMem, "an un-overridden app keeps the owner's defaults")
	assert.NotEqual(t, 4096, defDisk)
}
