package node

import (
	"bufio"

	"fmt"
	"github.com/hashicorp/yamux"
	"net"
	"net/http"

	"heckel.io/hostit/app"
)

// The dial-in path: hostit-node dials control's internal listener, sends one
// upgrade request, and the raw connection becomes the duplex -- control ends
// up holding a NodeAgent for the node, the node serves its RPC over it.

// connectPath is the internal upgrade endpoint.
const connectPath = "/internal/node/connect"

// ConnectHandler is control's side: it hijacks the upgrade request's
// connection, becomes the duplex's accepting side, and hands the registered
// remote agent (plus the node id) to register. The node id comes from the
// mTLS client cert CN when the transport carries one, else from the node's
// self-reported header (acceptable only on the root-only local socket).
func ConnectHandler(register func(nodeID string, agent app.NodeAgent)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectPath && r.URL.Path != "/" { // mounted standalone in tests
			http.NotFound(w, r)
			return
		}
		nodeID := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			nodeID = r.TLS.PeerCertificates[0].Subject.CommonName
		} else {
			nodeID = r.Header.Get("X-Hostit-Node")
		}
		if nodeID == "" {
			http.Error(w, "no node identity", http.StatusForbidden)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = buf.Flush()
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n"))
		client, err := Duplex(conn, false, nil)
		if err != nil {
			_ = conn.Close()
			return
		}
		register(nodeID, NewRemoteAgent(client))
	})
}

// ServeAgent is the node's side after dialing: it sends the upgrade request
// on the raw connection, then serves its NodeAgent over the duplex. Blocks
// until the connection dies (the caller redials with backoff).
func ServeAgent(conn net.Conn, nodeID string, agent app.NodeAgent) error {
	req, err := http.NewRequest("POST", "http://control"+connectPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Hostit-Node", nodeID)
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("connect refused: %s", resp.Status)
	}
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		return err
	}
	go func() {
		_ = http.Serve(sess, RPCHandler(agent))
	}()
	<-sess.CloseChan() // returns when the connection dies; the caller redials
	return nil
}
