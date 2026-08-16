package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlHasServeAndNode(t *testing.T) {
	app := newControlApp()
	require.NotNil(t, app.Command("serve"), "hostit-control runs the daemon")
	require.NotNil(t, app.Command("node"), "hostit-control owns the node registry")
}
