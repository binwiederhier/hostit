package link

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/proxy/api"
)

// fakeProxy records what control pushed at it.
type fakeProxy struct {
	applied chan *api.Table
}

func (f *fakeProxy) ApplyRoutes(table *api.Table) error {
	f.applied <- table
	return nil
}

func (f *fakeProxy) Heartbeat() *api.Heartbeat {
	return &api.Heartbeat{Version: "v1.2.3", Routes: 7}
}

// fakeControl answers the proxy's cert requests.
type fakeControl struct {
	asked chan string
}

func (f *fakeControl) CertFor(sni string) (*api.CertMaterial, error) {
	f.asked <- sni
	if sni == "nobody.example.com" {
		return nil, api.ErrNoCert
	}
	return &api.CertMaterial{CertPEM: "cert:" + sni, KeyPEM: "key:" + sni}, nil
}

// link stands up a real duplex between a control side and a proxy side, the
// way the cluster transport does in production, and hands back both views.
func link(t *testing.T, agent api.ProxyAgent, sink api.ControlSink) (api.ProxyAgent, api.ControlSink) {
	t.Helper()
	remote := make(chan api.ProxyAgent, 1)
	sockPath := filepath.Join(t.TempDir(), "cluster.sock")
	ln, err := cluster.ListenSocket(sockPath, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	srv := cluster.SocketServer(cluster.ConnectHandler(map[string]*cluster.Role{
		cluster.RoleProxy: Role(func(string) bool { return true }, sink, func(_ string, p api.ProxyAgent) {
			remote <- p
		}, nil),
	}))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := cluster.DialSocket(sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	toControl := make(chan api.ControlSink, 1)
	go func() {
		_ = ServeAgent(conn, "edge-1", agent, func(client *http.Client) {
			toControl <- NewControlSink(client)
		})
	}()
	select {
	case p := <-remote:
		select {
		case c := <-toControl:
			return p, c
		case <-time.After(5 * time.Second):
			t.Fatal("the proxy never got its link to control")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy never registered")
	}
	return nil, nil
}

// Control states what the proxy should serve and the proxy applies it: the
// same direction of authority a node's desired state travels in.
func TestControlPushesTheRoutingTable(t *testing.T) {
	proxy := &fakeProxy{applied: make(chan *api.Table, 1)}
	remote, _ := link(t, proxy, &fakeControl{asked: make(chan string, 1)})

	table := &api.Table{Seq: 42, Routes: []api.Route{
		{Host: "blog.apps.example.com", Target: "10.0.0.4:10000"},
	}}
	require.NoError(t, remote.ApplyRoutes(table))

	select {
	case got := <-proxy.applied:
		assert.Equal(t, int64(42), got.Seq)
		require.Len(t, got.Routes, 1)
		assert.Equal(t, "blog.apps.example.com", got.Routes[0].Host)
		assert.Equal(t, "10.0.0.4:10000", got.Routes[0].Target)
	case <-time.After(5 * time.Second):
		t.Fatal("the table never arrived")
	}

	hb := remote.Heartbeat()
	require.NotNil(t, hb)
	assert.Equal(t, "v1.2.3", hb.Version)
	assert.Equal(t, 7, hb.Routes)
}

// Certificates travel the other way: the trigger is a handshake for a name the
// proxy has never seen, which is exactly when control may still have to issue
// one. It rides the connection the proxy already dialed.
func TestTheProxyAsksControlForACertificate(t *testing.T) {
	control := &fakeControl{asked: make(chan string, 2)}
	_, sink := link(t, &fakeProxy{applied: make(chan *api.Table, 1)}, control)

	mat, err := sink.CertFor("blog.apps.example.com")
	require.NoError(t, err)
	require.NotNil(t, mat)
	assert.Equal(t, "cert:blog.apps.example.com", mat.CertPEM)
	assert.Equal(t, "key:blog.apps.example.com", mat.KeyPEM)
	assert.Equal(t, "blog.apps.example.com", <-control.asked)

	// An unknown name is a definite "not ours", not a transport failure: the
	// proxy must be able to tell those apart to decide whether to fall back.
	_, err = sink.CertFor("nobody.example.com")
	assert.ErrorIs(t, err, api.ErrNoCert)
}
