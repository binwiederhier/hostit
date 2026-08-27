package node

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForAppReturnsWhenPortAccepts(t *testing.T) {
	t.Parallel()
	m := newSweepTestMachine(t, &recordRunner{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	assert.True(t, m.waitForAppUntil(port, time.Now().Add(2*time.Second)), "a listening port is seen as ready")
}

func TestWaitForAppGivesUpWhenNothingListens(t *testing.T) {
	t.Parallel()
	m := newSweepTestMachine(t, &recordRunner{})
	// A port nothing listens on: the wait must return false by the deadline
	// rather than block the deploy forever.
	assert.False(t, m.waitForAppUntil(59321, time.Now().Add(150*time.Millisecond)))
}
