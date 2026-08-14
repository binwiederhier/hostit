package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

func TestRenameAppIsCheapAndPreservesTheContainer(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	id := a.ID
	oldSubvol := m.appSubvolume("blog")
	// Attach a token so we can prove per-app rows follow the rename.
	require.NoError(t, m.store.AddToken(&store.Token{UserID: "u_1", Hash: "h1", AppName: "blog", Secret: "sek"}))
	runner.reset()

	shop, err := m.RenameApp("blog", "shop")
	require.NoError(t, err)
	assert.Equal(t, "shop", shop.Name)
	assert.Equal(t, id, shop.ID, "the id is stable across a rename")

	// The old name is gone; the new one resolves.
	_, err = m.App("blog")
	require.ErrorIs(t, err, store.ErrAppNotFound)
	got, err := m.App("shop")
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)

	// Durable resources are unchanged: the id-keyed subvolume, container and unit
	// are the same as before, so nothing had to move.
	assert.Equal(t, oldSubvol, m.appSubvolume("shop"))
	assert.Equal(t, workspace.ContainerName(id), m.containerName("shop"))
	assert.Equal(t, workspace.UnitName(id), m.unitName("shop"))

	// The critical property: the (stateful) container is NOT recreated.
	ran := runner.ran()
	assert.NotContains(t, ran, "podman rm", "a rename must not tear down the container")
	assert.NotContains(t, ran, "podman create", "a rename must not recreate the container")
	assert.NotContains(t, ran, "systemctl restart", "a rename must not restart the app")

	// The only OS mutation is the Unix login rename.
	assert.Contains(t, ops.renamedUsers, "blog->shop")

	// Per-app rows follow the rename (they key on app_id).
	tokens, err := m.store.TokensByApp("shop")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, "shop", tokens[0].AppName)
}

func TestRenameStopsAndStartsARunningApp(t *testing.T) {
	t.Parallel()
	m, ops, runner := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	unit := workspace.UnitName(a.ID)
	// The app is running: usermod would be blocked by the container/session, so the
	// rename must stop the app around the usermod and start it again afterwards.
	runner.returns("is-active", "active")
	runner.reset()

	_, err := m.RenameApp("blog", "shop")
	require.NoError(t, err)

	ran := runner.ran()
	assert.Contains(t, ran, "systemctl stop "+unit, "a running app is stopped for the usermod")
	assert.Contains(t, ran, "systemctl start "+unit, "and started again afterwards")
	// Leftover processes owned by the app user are force-killed so usermod is not blocked.
	assert.Contains(t, ops.killedUsers, "blog")
	// Still no recreate: stop/start reuses the same container, so state is kept.
	assert.NotContains(t, ran, "podman rm")
	assert.NotContains(t, ran, "podman create")
	assert.Contains(t, ops.renamedUsers, "blog->shop")
}

func TestRenameAppRejectsATakenName(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	createTestApp(t, m, "shop")
	_, err := m.RenameApp("blog", "shop")
	require.ErrorIs(t, err, ErrAppExists)
	// The original is untouched.
	_, err = m.App("blog")
	require.NoError(t, err)
}
