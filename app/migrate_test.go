package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestMigrateGroupNamesAlignsEachApp(t *testing.T) {
	t.Parallel()
	m, ops := newTestManager(t)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal}))

	// The migration asks the OS layer to align each app's group name to its user.
	m.MigrateGroupNames()

	assert.ElementsMatch(t, []string{"blog", "wiki"}, ops.syncedGroups)
}
