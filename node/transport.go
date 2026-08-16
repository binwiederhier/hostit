// Package node is the multi-node IPC layer: a control-owned CA issuing
// per-node identity certificates (the CN is the node id), and a duplex
// transport that carries ordinary HTTP in both directions over one mTLS
// connection the node dialed. Control never dials nodes; a node needs no
// public listener. See plans/260807-hostit-multinode.md, section 4.
package node

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/yamux"
)

// Duplex turns one established connection into HTTP both ways: it serves
// handler for requests the PEER opens, and returns a client for making
// requests TO the peer. Exactly one side passes dialer=true (the side that
// dialed the underlying connection); yamux only uses it to assign stream-id
// parity -- both sides can open and accept streams.
func Duplex(conn net.Conn, dialer bool, handler http.Handler) (*http.Client, *yamux.Session, error) {
	var sess *yamux.Session
	var err error
	if dialer {
		sess, err = yamux.Client(conn, nil)
	} else {
		sess, err = yamux.Server(conn, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	// A yamux session is a net.Listener whose Accept returns peer-opened
	// streams, so an ordinary http.Server serves the reverse direction.
	go func() {
		_ = http.Serve(sess, handler)
	}()
	return duplexClient(sess), sess, nil
}

// rpcTimeout bounds a single duplex request. yamux's transport keepalive keeps
// the session healthy even when the far handler is wedged, so without a
// per-request deadline a hung verb blocks its caller forever. Generous on
// purpose: it must clear the slowest legitimate one-shot RPC (a large tar
// upload or file read on a loaded box), so it is a hang backstop, not a SLA.
// A var, not a const, so tests can shorten it.
var rpcTimeout = 5 * time.Minute

// duplexClient builds the http.Client that carries requests to the peer, each
// on its own yamux stream, bounded by rpcTimeout.
func duplexClient(sess *yamux.Session) *http.Client {
	return &http.Client{
		Timeout: rpcTimeout,
		Transport: &http.Transport{
			// Every request rides its own stream; the URL host is cosmetic.
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return sess.OpenStream()
			},
			// Streams are cheap and the session multiplexes; per-stream keep-alive
			// would just pin stale streams.
			DisableKeepAlives: true,
		},
	}
}
