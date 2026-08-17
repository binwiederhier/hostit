package cluster

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A member's role is minted into its certificate, so it is proven by the
// transport rather than claimed at connect time. A proxy credential can
// therefore never register as a node, whatever it says about itself.
func TestACertificateCarriesItsRole(t *testing.T) {
	ca, err := NewCA()
	require.NoError(t, err)

	proxyCert, err := ca.Issue("edge-1", RoleProxy)
	require.NoError(t, err)
	require.NotNil(t, proxyCert.Leaf)
	assert.Equal(t, RoleProxy, roleOf(proxyCert.Leaf))

	nodeCert, err := ca.Issue("worker-2", RoleNode)
	require.NoError(t, err)
	assert.Equal(t, RoleNode, roleOf(nodeCert.Leaf))
}

// Node certificates minted before roles existed are still in the field and
// carry no OU at all. They are nodes, which is what they were issued as.
func TestACertificateWithoutARoleIsANode(t *testing.T) {
	ca, err := NewCA()
	require.NoError(t, err)
	cert, err := ca.Issue("worker-2", "")
	require.NoError(t, err)
	assert.Equal(t, RoleNode, roleOf(cert.Leaf))
}

// One listener, several kinds of member: the connect handler routes a peer to
// the Role its certificate names, and refuses a role nobody registered.
func TestConnectHandlerRoutesByRole(t *testing.T) {
	admitted := make(chan Peer, 2)
	role := func() *Role {
		return &Role{
			Authorize: func(Peer) bool { return true },
			Register: func(peer Peer, _ *http.Client) func() {
				admitted <- peer
				return nil
			},
		}
	}
	srv := httptest.NewServer(ConnectHandler(map[string]*Role{RoleNode: role(), RoleProxy: role()}))
	defer srv.Close()

	// The local socket has no certificate, so the role rides a header there.
	for _, want := range []string{RoleNode, RoleProxy} {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		require.NoError(t, err)
		go func() { _ = Serve(conn, Peer{ID: "member-1", Role: want}, nil, nil) }()
		select {
		case peer := <-admitted:
			assert.Equal(t, want, peer.Role)
			assert.Equal(t, "member-1", peer.ID)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was never admitted", want)
		}
		_ = conn.Close()
	}

	// A role nobody registered is refused rather than defaulted.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	err = Serve(conn, Peer{ID: "member-1", Role: "auditor"}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}
