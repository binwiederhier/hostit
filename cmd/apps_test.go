package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
