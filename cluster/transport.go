// Package cluster is how the parts of a hostit cluster reach each other: the
// control-owned CA that issues a member's identity certificate (the CN is the
// member's id, the OU its role), and the duplex transport that carries
// ordinary HTTP in both directions over the one mTLS connection that member
// dialed. Control never dials a member; a member needs no listener.
//
// It knows nothing about what nodes or proxies actually say to each other --
// those contracts live in nodeapi and proxyapi, and their wire layers in
// nodelink and link. Keeping this package free of them is what lets
// hostit-proxy speak the cluster protocol without linking the registry (and
// with it SQLite) into a binary that has no database.
package cluster

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
	return DuplexClient(sess), sess, nil
}

// rpcTimeout bounds a single duplex request. yamux's transport keepalive keeps
// the session healthy even when the far handler is wedged, so without a
// per-request deadline a hung verb blocks its caller forever. Generous on
// purpose: it must clear the slowest legitimate one-shot RPC (a large tar
// upload or file read on a loaded box), so it is a hang backstop, not a SLA.
// A var, not a const, so tests can shorten it.
var (
	rpcTimeout = 5 * time.Minute
)

// DuplexClient builds the http.Client that carries requests to the peer, each
// on its own yamux stream, bounded by rpcTimeout.
func DuplexClient(sess *yamux.Session) *http.Client {
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
