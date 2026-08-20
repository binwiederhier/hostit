package cluster

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
)

// The dial-in path: a cluster member dials control's listener, sends one
// upgrade request, and the raw connection becomes the duplex -- control ends
// up holding an http.Client aimed at the member, and the member serves its own
// RPC over the same connection. Control never dials a member; a member needs
// no listener of its own.

const (
	// connectPath is the internal upgrade endpoint.
	connectPath = "/internal/node/connect"
	// ControlID is control's identity: the CN of its cert, the ServerName a
	// member pins, and the cosmetic host in the reverse-HTTP URLs.
	ControlID = "control"
	// peerHeader/roleHeader carry a member's self-reported identity on the
	// cert-less local socket; over TLS the certificate carries both instead.
	peerHeader = "X-Hostit-Node"
	roleHeader = "X-Hostit-Role"
)

const (
	// RoleNode and RoleProxy are the kinds of member that dial in. The role is
	// part of the identity, not a claim the member makes: it is minted into the
	// certificate, so a proxy's credential cannot register as a node.
	RoleNode  = "node"
	RoleProxy = "proxy"
)

// Peer is the far side of a cluster connection, as the transport proved it.
type Peer struct {
	ID   string
	Role string
}

// Role is how one kind of member is admitted. Authorize decides whether this
// peer may connect at all, Callbacks is what the peer may call back through,
// and Register hands over the client aimed at the peer -- plus a dialer for
// raw streams on the same session, which is what an interactive terminal
// rides: a pty is a byte stream, not a request -- and returns the cleanup to
// run when the connection dies.
type Role struct {
	Authorize func(peer Peer) bool
	Callbacks func(peer Peer) http.Handler
	Register  func(peer Peer, client *http.Client, dial func() (net.Conn, error)) (onClose func())
}

// ConnectHandler is control's side: it hijacks the upgrade request's
// connection, becomes the duplex's accepting side, and hands the peer to the
// Role registered for its kind. An unknown role is refused, so adding a role
// here is the only way to admit one.
//
// Over TLS the identity is the client certificate, full stop. The self-reported
// headers are honored only WITHOUT TLS, which is the root-only local socket --
// anywhere else they would let a caller claim any identity it liked.
func ConnectHandler(roles map[string]*Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectPath && r.URL.Path != "/" { // mounted standalone in tests
			http.NotFound(w, r)
			return
		}
		peer := peerFrom(r)
		role, known := roles[peer.Role]
		if peer.ID == "" || !known || !role.Authorize(peer) {
			http.Error(w, "no cluster identity", http.StatusForbidden)
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
		if role.Callbacks != nil {
			cb = role.Callbacks(peer)
		}
		client, sess, err := Duplex(conn, false, cb)
		if err != nil {
			_ = conn.Close()
			return
		}
		onClose := role.Register(peer, client, func() (net.Conn, error) { return sess.OpenStream() })
		if onClose != nil {
			go func() {
				<-sess.CloseChan()
				onClose()
			}()
		}
	})
}

// peerFrom reads the far side's identity. Over TLS that is the certificate.
// Over the same-host socket it is the kernel: the connecting process must be
// root, and only then are its self-reported name and role believed. Without the
// uid check the headers would be a free-for-all -- anything that could open the
// socket could claim to be any node.
func peerFrom(r *http.Request) Peer {
	if r.TLS != nil {
		if len(r.TLS.PeerCertificates) == 0 {
			return Peer{}
		}
		cert := r.TLS.PeerCertificates[0]
		return Peer{ID: cert.Subject.CommonName, Role: roleOf(cert)}
	}
	uid, ok := socketPeerUID(r)
	if !ok || !trustedPeerUID(uid) {
		return Peer{}
	}
	peer := Peer{ID: r.Header.Get(peerHeader), Role: r.Header.Get(roleHeader)}
	if peer.Role == "" {
		peer.Role = RoleNode
	}
	return peer
}

// roleOf reads the role out of a certificate's OU. A certificate without one
// is a node: node certificates minted before roles existed are still in the
// field, and every one of them belongs to a node.
func roleOf(cert *x509.Certificate) string {
	if len(cert.Subject.OrganizationalUnit) == 0 {
		return RoleNode
	}
	return cert.Subject.OrganizationalUnit[0]
}

// Serve is the member's side after dialing: it sends the upgrade request on
// the raw connection, then serves handler over the duplex. onLink receives the
// client aimed at control, for the member's own calls back. Blocks until the
// connection dies (the caller redials with backoff).
func Serve(conn net.Conn, peer Peer, handler http.Handler, onLink func(client *http.Client)) error {
	req, err := http.NewRequest("POST", "http://"+ControlID+connectPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set(peerHeader, peer.ID)
	req.Header.Set(roleHeader, peer.Role)
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
		_ = http.Serve(sess, handler)
	}()
	// The reverse direction: the member's own requests to control ride the same
	// session, bounded by the same deadline.
	if onLink != nil {
		onLink(DuplexClient(sess))
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
