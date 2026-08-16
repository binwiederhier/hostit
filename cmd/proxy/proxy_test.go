package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyCLIHasServe(t *testing.T) {
	app := newProxyApp("v0.0.0-test")
	serve := app.Command("serve")
	require.NotNil(t, serve, "hostit-proxy's one job is serve")
	// The config flag is how the unit points it at proxy.yml.
	names := make([]string, 0)
	for _, f := range serve.Flags {
		names = append(names, f.Names()...)
	}
	assert.Contains(t, names, "config")
}
