package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
)

func TestControlHasNodeCommands(t *testing.T) {
	control := NewControl()
	nodes := control.Command("node")
	require.NotNil(t, nodes, "hostit-control manages the node registry")
	names := make([]string, 0)
	for _, c := range nodes.Subcommands {
		names = append(names, c.Name)
	}
	for _, expected := range []string{"add", "list", "remove"} {
		assert.Contains(t, names, expected)
	}
}

func TestNodeAddMintsAJoinToken(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Control has started once: CA and registry exist.
	_, _, err := node.EnsureIPCCreds(dir)
	require.NoError(t, err)
	s, err := store.NewStore(filepath.Join(dir, "hostit.db"))
	require.NoError(t, err)
	defer s.Close()

	token, err := mintNodeJoinToken(s, dir, "worker-2", "10.0.0.2")
	require.NoError(t, err)

	// The token parses, pins the CA, and its hash is redeemable in the registry.
	name, secret, caFP, err := node.ParseJoinToken(token)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", name)
	ca, err := node.LoadCA(dir)
	require.NoError(t, err)
	assert.Equal(t, ca.Fingerprint(), caFP)
	_ = secret

	n, err := s.Node("worker-2")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2", n.Address)
	assert.True(t, n.JoinedAt.IsZero(), "pending until the token is used")
	_ = os.Remove(filepath.Join(dir, "hostit.db"))
}
