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

	"github.com/hashicorp/yamux"
)

// Duplex turns one established connection into HTTP both ways: it serves
// handler for requests the PEER opens, and returns a client for making
// requests TO the peer. Exactly one side passes dialer=true (the side that
// dialed the underlying connection); yamux only uses it to assign stream-id
// parity -- both sides can open and accept streams.
func Duplex(conn net.Conn, dialer bool, handler http.Handler) (*http.Client, error) {
	var sess *yamux.Session
	var err error
	if dialer {
		sess, err = yamux.Client(conn, nil)
	} else {
		sess, err = yamux.Server(conn, nil)
	}
	if err != nil {
		return nil, err
	}
	// A yamux session is a net.Listener whose Accept returns peer-opened
	// streams, so an ordinary http.Server serves the reverse direction.
	go func() {
		_ = http.Serve(sess, handler)
	}()
	return &http.Client{Transport: &http.Transport{
		// Every request rides its own stream; the URL host is cosmetic.
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return sess.OpenStream()
		},
		// Streams are cheap and the session multiplexes; per-stream keep-alive
		// would just pin stale streams.
		DisableKeepAlives: true,
	}}, nil
}
