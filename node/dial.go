package node

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
	"heckel.io/hostit/app"
)

// The dial-in path: hostit-node dials control's internal listener, sends one
// upgrade request, and the raw connection becomes the duplex -- control ends
// up holding a NodeAgent for the node, the node serves its RPC over it.

const (
	// connectPath is the internal upgrade endpoint.
	connectPath = "/internal/node/connect"
	// controlID is control's identity: the CN of its cert, the ServerName the
	// node pins, and the cosmetic host in the reverse-HTTP URLs.
	controlID = "control"
	// nodeHeader carries the node's self-reported id on the cert-less local
	// socket (over TLS the id is the client cert CN instead).
	nodeHeader = "X-Hostit-Node"
	// errHeader/errCodeHeader carry a verb's failure across the raw file-stream
	// responses (the JSON verbs use the rpcResp envelope instead).
	errHeader     = "X-Hostit-Err"
	errCodeHeader = "X-Hostit-Err-Code"
)

// ConnectHandler is control's side: it hijacks the upgrade request's
// connection, becomes the duplex's accepting side, and hands the registered
// remote agent (plus the node id) to register. Over TLS the node id is the
// client cert CN, full stop -- the listener admits cert-less connections for
// /join, so a header fallback there would let anyone claim any node. The
// self-reported header is acceptable only without TLS (the root-only local
// socket). authorize checks the id against the node registry: an unregistered
// node's still-valid certificate is refused, which is what makes `node
// remove` an effective revocation.
func ConnectHandler(authorize func(nodeID string) bool, callbacks func(nodeID string) http.Handler, register func(nodeID string, agent app.NodeAgent), disconnect func(nodeID string, agent app.NodeAgent)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectPath && r.URL.Path != "/" { // mounted standalone in tests
			http.NotFound(w, r)
			return
		}
		nodeID := ""
		if r.TLS != nil {
			if len(r.TLS.PeerCertificates) > 0 {
				nodeID = r.TLS.PeerCertificates[0].Subject.CommonName
			}
		} else {
			nodeID = r.Header.Get(nodeHeader)
		}
		if nodeID == "" || !authorize(nodeID) {
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
		var cb http.Handler
		if callbacks != nil {
			cb = callbacks(nodeID)
		}
		client, sess, err := Duplex(conn, false, cb)
		if err != nil {
			_ = conn.Close()
			return
		}
		agent := NewRemoteAgent(client)
		register(nodeID, agent)
		if disconnect != nil {
			go func() {
				<-sess.CloseChan()
				disconnect(nodeID, agent)
			}()
		}
	})
}

// ServeAgent is the node's side after dialing: it sends the upgrade request
// on the raw connection, then serves its NodeAgent over the duplex. Blocks
// until the connection dies (the caller redials with backoff).
func ServeAgent(conn net.Conn, nodeID string, agent app.NodeAgent, onLink func(client *http.Client)) error {
	req, err := http.NewRequest("POST", "http://"+controlID+connectPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set(nodeHeader, nodeID)
	if err := req.Write(conn); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("connect refused: %s", resp.Status)
	}
	// Control talks yamux immediately after the 101, so its first frames can
	// already sit in the read buffer; hand them to the session or they are
	// silently lost and control's first stream hangs forever.
	var sessConn net.Conn = conn
	if n := br.Buffered(); n > 0 {
		peek, err := br.Peek(n)
		if err != nil {
			return err
		}
		sessConn = &bufferedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(append([]byte(nil), peek...)), conn)}
	}
	sess, err := yamux.Client(sessConn, nil)
	if err != nil {
		return err
	}
	go func() {
		_ = http.Serve(sess, RPCHandler(agent))
	}()
	// The reverse direction: the node's own requests to control (the control
	// sink's callbacks) ride the same session, bounded by the same deadline.
	if onLink != nil {
		onLink(duplexClient(sess))
	}
	<-sess.CloseChan() // returns when the connection dies; the caller redials
	return nil
}

// bufferedConn replays bytes the response reader had already buffered before
// handing the connection to yamux.
type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
