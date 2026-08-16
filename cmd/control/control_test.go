package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlHasServeAndNode(t *testing.T) {
	app := newControlApp("v0.0.0-test")
	require.NotNil(t, app.Command("serve"), "hostit-control runs the daemon")
	require.NotNil(t, app.Command("node"), "hostit-control owns the node registry")
}

// The binary's version must reach the CLI app: hostit-control stamps it into
// every container it creates (fused mode) and records it as the agents'
// version, so an empty one bakes "" into container identity AND makes the
// stale-agent check match forever -- agents would never be restarted on an
// upgrade again, which is the failure RestartStaleAgents exists to prevent.
func TestControlAppCarriesItsVersion(t *testing.T) {
	app := newControlApp("v1.2.3")
	require.Equal(t, "v1.2.3", app.Version)
}
