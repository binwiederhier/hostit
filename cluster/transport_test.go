package cluster

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDuplexHTTPBothWays proves the core trick: one mTLS connection, dialed by
// the node, carrying ordinary HTTP in BOTH directions (control->node commands
// and node->control reports).
func TestDuplexHTTPBothWays(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	controlCert, err := ca.Issue("control", RoleNode)
	require.NoError(t, err)
	nodeCert, err := ca.Issue("node-b", RoleNode)
	require.NoError(t, err)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerTLS(controlCert, ca.Pool()))
	require.NoError(t, err)
	defer ln.Close()

	// Control side: accept, learn the node id from the verified cert, serve its
	// own handlers, and get a client for commanding the node.
	type accepted struct {
		client *http.Client
		nodeID string
	}
	acceptedCh := make(chan accepted, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		tlsConn := conn.(*tls.Conn)
		if err := tlsConn.Handshake(); err != nil {
			errCh <- err
			return
		}
		id := tlsConn.ConnectionState().PeerCertificates[0].Subject.CommonName
		mux := http.NewServeMux()
		mux.HandleFunc("/heartbeat", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "noted")
		})
		client, _, err := Duplex(conn, false, mux)
		if err != nil {
			errCh <- err
			return
		}
		acceptedCh <- accepted{client, id}
	}()

	// Node side: dial with its own cert, serve the NodeAgent-ish handlers, and
	// get a client for reporting to control.
	conn, err := tls.Dial("tcp", ln.Addr().String(), ClientTLS(nodeCert, ca.Pool()))
	require.NoError(t, err)
	nodeMux := http.NewServeMux()
	nodeMux.HandleFunc("/provision", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "provisioned %s", string(b))
	})
	toControl, _, err := Duplex(conn, true, nodeMux)
	require.NoError(t, err)

	var ctl accepted
	select {
	case ctl = <-acceptedCh:
	case err := <-errCh:
		t.Fatalf("control side failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("control side never accepted")
	}
	assert.Equal(t, "node-b", ctl.nodeID, "the node id comes from the verified cert CN")

	// control -> node (reverse HTTP over the node-dialed connection)
	resp, err := ctl.client.Post("http://node/provision", "text/plain", nil)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(b), "provisioned")

	// node -> control on the same connection
	resp, err = toControl.Get("http://control/heartbeat")
	require.NoError(t, err)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "noted", string(b))
}

// TestMTLSRejectsUnknownClients: a dialer without a CA-signed cert never gets a
// session -- the privileged surface is unreachable without enrollment.
func TestMTLSRejectsUnknownClients(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	controlCert, err := ca.Issue("control", RoleNode)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerTLS(controlCert, ca.Pool()))
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Server-side handshake happens on first read; just try to read.
			buf := make([]byte, 1)
			_, _ = conn.Read(buf)
			_ = conn.Close()
		}
	}()

	// No client cert at all: the handshake (or first use) must fail.
	otherCA, err := NewCA()
	require.NoError(t, err)
	impostor, err := otherCA.Issue("node-x", RoleNode)
	require.NoError(t, err)
	for _, conf := range []*tls.Config{
		{RootCAs: ca.Pool().Clone(), ServerName: "control"},
		ClientTLS(impostor, ca.Pool()), // signed by a DIFFERENT CA
	} {
		conn, err := tls.Dial("tcp", ln.Addr().String(), conf)
		if err == nil {
			// TLS 1.3 reports client-cert rejection on first read/write
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = conn.Write([]byte("x"))
			if err == nil {
				_, err = conn.Read(make([]byte, 1))
			}
			_ = conn.Close()
		}
		assert.Error(t, err, "an unenrolled client must not get a usable connection")
	}
}

func TestDuplexClientTimesOutOnAWedgedHandler(t *testing.T) {
	t.Parallel()
	// A hung node handler must not block its control-side caller forever: yamux
	// keepalive keeps the session "healthy", so without a client deadline the
	// request never returns.
	old := rpcTimeout
	rpcTimeout = 200 * time.Millisecond
	defer func() { rpcTimeout = old }()

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	block := make(chan struct{})
	defer close(block)
	serverMux := http.NewServeMux()
	serverMux.HandleFunc("/wedge", func(w http.ResponseWriter, r *http.Request) { <-block })
	_, _, err := Duplex(c1, false, serverMux)
	require.NoError(t, err)
	client, _, err := Duplex(c2, true, http.NewServeMux())
	require.NoError(t, err)

	start := time.Now()
	_, err = client.Post("http://peer/wedge", "text/plain", nil)
	require.Error(t, err, "the wedged handler must surface as a client error, not a hang")
	assert.Less(t, time.Since(start), 3*time.Second, "the call must give up around the timeout")
}
