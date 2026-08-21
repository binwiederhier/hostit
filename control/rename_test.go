package control

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
	oldSubvol := m.testMachine().AppSubvolume("blog")
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
	assert.Equal(t, oldSubvol, m.testMachine().AppSubvolume("shop"))
	assert.Equal(t, workspace.ContainerName(id), m.testMachine().ContainerName("shop"))
	assert.Equal(t, workspace.UnitName(id), m.testMachine().UnitName("shop"))

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

// renameAgent records the Rename verbs control sends to the node agent, and can
// run a hook mid-rename to reproduce the flip race deterministically.
type renameAgent struct {
	NodeAgent
	renames [][3]string // old, new, id
	onFirst func()      // runs once, after the first machine rename succeeds
	fired   bool
}

func (a *renameAgent) Rename(oldName, newName, id string) error {
	if err := a.NodeAgent.Rename(oldName, newName, id); err != nil {
		return err
	}
	a.renames = append(a.renames, [3]string{oldName, newName, id})
	if a.onFirst != nil && !a.fired {
		a.fired = true
		a.onFirst()
	}
	return nil
}

// A rename is machine work and must go through the node agent (the routing
// agent in split mode), not through control's own promoted machine: renaming
// an app hosted on another node used to run usermod on the wrong host.
func TestRenameRoutesThroughTheNodeAgent(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	agent := &renameAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, agent)

	shop, err := m.RenameApp("blog", "shop")
	require.NoError(t, err)
	assert.Equal(t, "shop", shop.Name)
	require.Len(t, agent.renames, 1)
	assert.Equal(t, [3]string{"blog", "shop", a.ID}, agent.renames[0])
}

// If the registry flip loses the race to a same-name create, the machine
// rename is undone through the SAME agent, so login and registry stay
// consistent on whatever node the app lives on.
func TestRenameRestoresLoginWhenFlipFails(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	agent := &renameAgent{NodeAgent: m.testMachine()}
	// After the machine half succeeds, a rival app claims the target name --
	// the racy window between control's validation and the registry flip.
	agent.onFirst = func() {
		require.NoError(t, m.store.AddApp(&store.App{Name: "shop", Port: 10099, Host: store.HostLocal}))
		m.PushMirror()
	}
	m.NodeRegistry().Register(store.HostLocal, agent)

	_, err := m.RenameApp("blog", "shop")
	require.ErrorIs(t, err, ErrAppExists)
	// The machine rename was compensated: blog -> shop, then shop -> blog.
	require.Len(t, agent.renames, 2)
	assert.Equal(t, [3]string{"blog", "shop", a.ID}, agent.renames[0])
	assert.Equal(t, [3]string{"shop", "blog", a.ID}, agent.renames[1])
	// Registry still has the app under its old name; the login is back too.
	_, err = m.App("blog")
	require.NoError(t, err)
	assert.Contains(t, ops.renamedUsers, "blog->shop")
	assert.Contains(t, ops.renamedUsers, "shop->blog")
}
