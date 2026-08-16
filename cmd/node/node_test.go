package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeCLIHasServe(t *testing.T) {
	app := newNodeApp("test")
	require.NotNil(t, app.Command("serve"), "hostit-node runs the machine half")
	require.Nil(t, app.Command("join"), "enrollment is gone: identity comes from config-file certs")
}
