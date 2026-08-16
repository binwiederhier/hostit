package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureNodeUpsertsWithoutToken(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// The colocated "local" node exists implicitly: joined from the start, no
	// token; ensure-style so every control start can call it.
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	n, err := s.Node("local")
	require.NoError(t, err)
	assert.False(t, n.JoinedAt.IsZero())

	nodes, err := s.Nodes()
	require.NoError(t, err)
	require.Len(t, nodes, 1)
}

func TestNodeSeenAndRemove(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.EnsureNode("local", "127.0.0.1"))
	seen := time.Now()
	require.NoError(t, s.SetNodeSeen("local", seen))
	n, err := s.Node("local")
	require.NoError(t, err)
	assert.WithinDuration(t, seen, n.LastSeen, time.Second)

	require.NoError(t, s.RemoveNode("local"))
	_, err = s.Node("local")
	require.ErrorIs(t, err, ErrNodeNotFound)
}
