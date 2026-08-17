package proxylink

import (
	"net"
	"net/http"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/proxyapi"
)

// The proxy's half of the cluster transport: what a proxy serves once its
// connection is up, and what control holds for it. The connection itself
// belongs to package cluster.

// Role is how control admits a proxy. Unlike a node there is no registry row
// to check: a proxy holds no apps and owns no state, so its CA-signed
// certificate is the whole membership test, and revoking one means re-minting
// the cluster CA.
func Role(authorize func(proxyID string) bool, sink proxyapi.ControlSink, register func(proxyID string, agent proxyapi.ProxyAgent), disconnect func(proxyID string, agent proxyapi.ProxyAgent)) *cluster.Role {
	return &cluster.Role{
		Authorize: func(peer cluster.Peer) bool { return authorize(peer.ID) },
		Callbacks: func(cluster.Peer) http.Handler {
			if sink == nil {
				return nil
			}
			return CallbackHandler(sink)
		},
		Register: func(peer cluster.Peer, client *http.Client) func() {
			agent := NewRemoteProxy(client)
			register(peer.ID, agent)
			if disconnect == nil {
				return nil
			}
			return func() { disconnect(peer.ID, agent) }
		},
	}
}

// ServeAgent is the proxy's side after dialing: it serves its ProxyAgent over
// the duplex and blocks until the connection dies. onLink receives the client
// the proxy asks control for certificates through.
func ServeAgent(conn net.Conn, proxyID string, agent proxyapi.ProxyAgent, onLink func(client *http.Client)) error {
	peer := cluster.Peer{ID: proxyID, Role: cluster.RoleProxy}
	return cluster.Serve(conn, peer, RPCHandler(agent), onLink)
}
