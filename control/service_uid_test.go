package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

func TestCreateRecordsTheAppUID(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	assert.Equal(t, workspace.UIDFor(m.config.PortMin, a.Port), a.UID, "the allocated uid block base is recorded on the row")
	row, err := m.store.App("blog")
	require.NoError(t, err)
	assert.Equal(t, workspace.UIDFor(m.config.PortMin, a.Port), row.UID)
}

func TestBackfillUIDsFillsOnlyZeroRows(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	// A pre-uid-column row (uid 0) and a row that already has one
	require.NoError(t, m.store.AddApp(&store.App{Name: "old", Port: 10007, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "done", Port: 10008, Host: store.HostLocal, UID: 42}))

	m.BackfillUIDs()

	old, err := m.store.App("old")
	require.NoError(t, err)
	assert.Equal(t, workspace.UIDFor(m.config.PortMin, 10007), old.UID, "a zero uid is backfilled from the port")
	done, err := m.store.App("done")
	require.NoError(t, err)
	assert.Equal(t, 42, done.UID, "an already-recorded uid is left alone")
}

func TestAllocatePortRotatesInsteadOfReusingImmediately(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	a := createTestApp(t, m, "first")
	require.NoError(t, m.DeleteApp("first"))
	m.WaitBackground()

	// The freed port's uid still owns a budget qgroup whose phantom bytes may
	// not have committed yet; immediate reuse started brand-new apps over their
	// disk cap (EDQUOT on the first mkdir). Rotate instead.
	b := createTestApp(t, m, "second")
	assert.NotEqual(t, a.Port, b.Port, "a just-freed port must not be handed out immediately")
	assert.Greater(t, b.Port, a.Port)
}
