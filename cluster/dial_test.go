package cluster

import (
	"heckel.io/hostit/cluster/clustertest"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A member's role is minted into its certificate, so it is proven by the
// transport rather than claimed at connect time. A proxy credential can
// therefore never register as a node, whatever it says about itself.
func TestACertificateCarriesItsRole(t *testing.T) {
	ca, err := clustertest.NewCA()
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
	ca, err := clustertest.NewCA()
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
			Register: func(peer Peer, _ *http.Client, _ func() (net.Conn, error)) func() {
				admitted <- peer
				return nil
			},
		}
	}
	// The same-host path: a unix socket, where the kernel says who is calling
	// and the header only says which name and role that caller claims.
	path := filepath.Join(t.TempDir(), "cluster.sock")
	ln, err := ListenSocket(path, 0o600)
	require.NoError(t, err)
	defer ln.Close()
	srv := SocketServer(ConnectHandler(map[string]*Role{RoleNode: role(), RoleProxy: role()}))
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	for _, want := range []string{RoleNode, RoleProxy} {
		conn, err := DialSocket(path)
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
	conn, err := DialSocket(path)
	require.NoError(t, err)
	defer conn.Close()
	err = Serve(conn, Peer{ID: "member-1", Role: "auditor"}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}
