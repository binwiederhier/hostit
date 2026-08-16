package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/nodelink"
	"heckel.io/hostit/store"
)

func TestControlHasNodeCommands(t *testing.T) {
	control := newControlApp("v0.0.0-test")
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

func TestNodeAddRegistersTheNode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Control has started once: CA and registry exist.
	_, _, err := nodelink.EnsureIPCCreds(dir)
	require.NoError(t, err)
	s, err := store.NewStore(filepath.Join(dir, "hostit.db"))
	require.NoError(t, err)
	defer s.Close()

	// The CLI's mint path: issue the node's certificate from the cluster CA
	// and register the row; the pair plus the row IS the membership.
	ca, err := nodelink.LoadCA(dir)
	require.NoError(t, err)
	cert, err := ca.Issue("worker-2")
	require.NoError(t, err)
	certPEM, keyPEM, err := nodelink.EncodeCert(cert)
	require.NoError(t, err)
	assert.Contains(t, certPEM, "BEGIN CERTIFICATE")
	assert.Contains(t, keyPEM, "BEGIN PRIVATE KEY")
	require.NoError(t, s.EnsureNode("worker-2", "10.0.0.2"))

	n, err := s.Node("worker-2")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2", n.Address)
}
