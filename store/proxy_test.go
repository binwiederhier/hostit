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

// A proxy's heartbeat carries what it is and how much it is serving, which is
// what makes `proxy list` as useful as `node list`: a proxy connected but
// serving an empty table is the interesting failure, and it is invisible if all
// you record is that it answered.
func TestProxyStatusRecordsBuildAndRoutes(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.EnsureProxy("edge-1"))

	seen := time.Now().Truncate(time.Second)
	require.NoError(t, s.SetProxyStatus("edge-1", seen, "v0.13.0", 7))

	p, err := s.Proxy("edge-1")
	require.NoError(t, err)
	assert.Equal(t, seen.Unix(), p.LastSeen.Unix())
	assert.Equal(t, "v0.13.0", p.Version)
	assert.Equal(t, 7, p.Routes)
}
