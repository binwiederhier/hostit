package link

import (
	"net"
	"net/http"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/api"
)

// The node's half of the cluster transport: what a node serves once its
// connection is up, and what control holds for it. The connection itself --
// mTLS, the upgrade, the yamux duplex -- belongs to package cluster, which
// knows nothing of node verbs.

// Role is how control admits a node: the registry row is the membership switch
// `hostit-control node add` flips on and `node remove` off, checked on top of
// the certificate the transport already verified. An unregistered node's
// still-valid certificate is refused, which is what makes removal effective.
func Role(authorize func(nodeID string) bool, callbacks func(nodeID string) http.Handler, register func(nodeID string, agent api.NodeAgent), disconnect func(nodeID string, agent api.NodeAgent)) *cluster.Role {
	return &cluster.Role{
		Authorize: func(peer cluster.Peer) bool { return authorize(peer.ID) },
		Callbacks: func(peer cluster.Peer) http.Handler {
			if callbacks == nil {
				return nil
			}
			return callbacks(peer.ID)
		},
		Register: func(peer cluster.Peer, client *http.Client, dial func() (net.Conn, error)) func() {
			// The agent is built once per connection and handed to both sides of
			// the lifecycle, so the disconnect that fires when this session dies
			// cannot unregister a NEWER connection's agent.
			agent := NewRemoteAgent(client, dial)
			register(peer.ID, agent)
			if disconnect == nil {
				return nil
			}
			return func() { disconnect(peer.ID, agent) }
		},
	}
}

// ServeAgent is the node's side after dialing: it serves its NodeAgent over
// the duplex and blocks until the connection dies. onLink receives the client
// for the node's own callbacks to control -- and nil when this connection
// ends, so nothing keeps posting into a dead session between redials.
func ServeAgent(conn net.Conn, nodeID string, agent api.NodeAgent, onLink func(client *http.Client)) error {
	defer onLink(nil)
	peer := cluster.Peer{ID: nodeID, Role: cluster.RoleNode}
	return cluster.Serve(conn, peer, RPCHandler(agent), onLink)
}
