package link

import (
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// When the control connection dies, the link must be told so: a stale client
// would make callbacks fail slowly instead of being dropped, and would make
// `hostit node status` claim a connection that is gone.
func TestServeAgentClearsTheLinkOnExit(t *testing.T) {
	t.Parallel()
	c1, c2 := net.Pipe()
	_ = c2.Close()
	_ = c1.Close()
	var calls []*http.Client
	_ = ServeAgent(c1, "n1", nil, func(client *http.Client) { calls = append(calls, client) })
	require.NotEmpty(t, calls, "the link hears about the connection's end")
	assert.Nil(t, calls[len(calls)-1], "the last word on exit is nil: the link is down")
}
