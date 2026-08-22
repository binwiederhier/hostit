package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

func TestAppPoweredOffFlagRoundTrips(t *testing.T) {
	t.Parallel()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	// Fresh apps are on; poweroff intent is explicit, recorded state -- not
	// something inferred from systemd (where "never enabled yet" and "disabled
	// on purpose" are indistinguishable).
	a, err := s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.PoweredOff)

	require.NoError(t, s.SetAppPoweredOff("blog", true))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.True(t, a.PoweredOff)

	apps, err := s.Apps()
	require.NoError(t, err)
	assert.True(t, apps[0].PoweredOff, "list queries carry the flag too")

	require.NoError(t, s.SetAppPoweredOff("blog", false))
	a, err = s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.PoweredOff)
}
