package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A proxy is a registry row for the same reason a node is: the row, not the
// certificate, is the membership switch. Removing a proxy has to stop its
// still-valid certificate from being accepted, or the only way to evict one
// would be to re-mint the cluster CA and every member with it.
func TestProxyRegistryIsTheMembershipSwitch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.EnsureProxy(ProxyLocal))
	require.NoError(t, s.EnsureProxy(ProxyLocal)) // every control start calls it
	require.NoError(t, s.EnsureProxy("edge-1"))

	p, err := s.Proxy("edge-1")
	require.NoError(t, err)
	assert.Equal(t, "edge-1", p.Name)
	assert.True(t, p.LastSeen.IsZero())

	proxies, err := s.Proxies()
	require.NoError(t, err)
	require.Len(t, proxies, 2)

	seen := time.Now().Truncate(time.Second)
	require.NoError(t, s.SetProxySeen("edge-1", seen))
	p, err = s.Proxy("edge-1")
	require.NoError(t, err)
	assert.Equal(t, seen.Unix(), p.LastSeen.Unix())

	require.NoError(t, s.DeleteProxy("edge-1"))
	_, err = s.Proxy("edge-1")
	assert.ErrorIs(t, err, ErrProxyNotFound)
}
