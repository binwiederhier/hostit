package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// subCommand finds a named subcommand (or alias) under a command, for asserting
// the shape of the apps command tree.
func subCommand(cmd *cli.Command, name string) *cli.Command {
	for _, c := range cmd.Subcommands {
		if c.Name == name {
			return c
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}

func TestAppsPowerAndSnapshotAreGrouped(t *testing.T) {
	t.Parallel()
	power := subCommand(cmdApps, "power")
	require.NotNil(t, power, "apps has a power group")
	for _, name := range []string{"on", "off", "reboot"} {
		assert.NotNil(t, subCommand(power, name), "power has %q", name)
	}
	snapshot := subCommand(cmdApps, "snapshot")
	require.NotNil(t, snapshot, "apps has a snapshot group")
	for _, name := range []string{"list", "create", "delete"} {
		assert.NotNil(t, subCommand(snapshot, name), "snapshot has %q", name)
	}
	// The old flat commands are gone, folded into the groups above
	for _, name := range []string{"poweron", "poweroff", "snapshots", "rmsnapshot"} {
		assert.Nil(t, subCommand(cmdApps, name), "%q should be grouped, not top-level", name)
	}
}

func TestResolveTransport(t *testing.T) {
	t.Parallel()
	// --host selects the remote API, which still needs a token
	tr, err := resolveTransport("https://hostit.apps.example.com", "tok", "/run/hostit/hostit.sock", false)
	require.NoError(t, err)
	assert.Equal(t, transportRemote, tr)
	_, err = resolveTransport("https://hostit.apps.example.com", "", "/run/hostit/hostit.sock", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--token")
	// No host: the local unix socket, if the daemon is running
	tr, err = resolveTransport("", "", "/run/hostit/hostit.sock", true)
	require.NoError(t, err)
	assert.Equal(t, transportSocket, tr)
	// A stray token without a host changes nothing; the socket is still local
	tr, err = resolveTransport("", "tok", "/run/hostit/hostit.sock", true)
	require.NoError(t, err)
	assert.Equal(t, transportSocket, tr)
	// No host and no socket: say where we looked and how to go remote instead
	_, err = resolveTransport("", "", "/run/hostit/hostit.sock", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/run/hostit/hostit.sock")
	assert.Contains(t, err.Error(), "--host")
}
