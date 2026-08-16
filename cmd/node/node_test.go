package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeCLIHasServeAndJoin(t *testing.T) {
	app := newNodeApp("v0.0.0-test")
	require.NotNil(t, app.Command("serve"), "the node daemon runs via serve")
	join := app.Command("join")
	require.NotNil(t, join, "a remote node enrolls via join")
	// join needs the control address and the one-time token.
	names := make([]string, 0)
	for _, f := range join.Flags {
		names = append(names, f.Names()...)
	}
	assert.Contains(t, names, "control")
	assert.Contains(t, names, "token")
}
