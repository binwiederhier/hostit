package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlHasNodeCommands(t *testing.T) {
	control := newControlApp("v0.0.0-test")
	nodes := control.Command("node")
	require.NotNil(t, nodes, "hostit-control manages the node registry")
	names := make([]string, 0)
	for _, c := range nodes.Subcommands {
		names = append(names, c.Name)
	}
	// No "add": a node enrolls by presenting a CA-signed cert (issued out of
	// band), so the CLI only lists and removes rows.
	for _, expected := range []string{"list", "remove"} {
		assert.Contains(t, names, expected)
	}
	assert.NotContains(t, names, "add")
}
