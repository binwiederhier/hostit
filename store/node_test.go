package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeJoinTokenLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	expires := time.Now().Add(time.Hour)

	// node add mints a pending row holding only the token HASH.
	require.NoError(t, s.CreateNode("worker-2", "10.0.0.2", "tokenhash", expires))
	n, err := s.Node("worker-2")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2", n.Address)
	assert.True(t, n.JoinedAt.IsZero(), "not joined yet")

	// The join consumes the token exactly once and marks the node joined.
	name, err := s.ConsumeNodeJoinToken("tokenhash", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "worker-2", name)
	_, err = s.ConsumeNodeJoinToken("tokenhash", time.Now())
	require.ErrorIs(t, err, ErrNodeJoinTokenInvalid, "single-use")

	n, err = s.Node("worker-2")
	require.NoError(t, err)
	assert.False(t, n.JoinedAt.IsZero())
}

func TestNodeJoinTokenExpires(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.CreateNode("worker-2", "", "tokenhash", time.Now().Add(-time.Minute)))
	_, err := s.ConsumeNodeJoinToken("tokenhash", time.Now())
	require.ErrorIs(t, err, ErrNodeJoinTokenInvalid)
}

func TestNodeReAddPendingRemintsToken(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	expires := time.Now().Add(time.Hour)
	require.NoError(t, s.CreateNode("worker-2", "10.0.0.2", "old", expires))
	// Re-adding a node that never joined replaces the token (lost token UX);
	// re-adding a JOINED node is an error (remove it first).
	require.NoError(t, s.CreateNode("worker-2", "10.0.0.3", "new", expires))
	name, err := s.ConsumeNodeJoinToken("new", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "worker-2", name)
	require.ErrorIs(t, s.CreateNode("worker-2", "", "again", expires), ErrNodeExists)
}

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
