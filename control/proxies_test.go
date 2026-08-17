package control

import (
	"crypto/tls"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/proxyapi"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

// recordingProxy stands in for a connected hostit-proxy.
type recordingProxy struct {
	tables chan *proxyapi.Table
}

func (r *recordingProxy) ApplyRoutes(table *proxyapi.Table) error {
	r.tables <- table
	return nil
}

func (r *recordingProxy) Heartbeat() *proxyapi.Heartbeat { return &proxyapi.Heartbeat{} }

// Control states the routing table at its proxies rather than answering polls
// for it: the same direction of authority a node's desired state travels in.
// A proxy that connects is handed the table straight away.
func TestPushRoutesReachesEveryConnectedProxy(t *testing.T) {
	s := newTestServer(t)
	a, b := &recordingProxy{tables: make(chan *proxyapi.Table, 4)}, &recordingProxy{tables: make(chan *proxyapi.Table, 4)}
	s.Proxies().Register("edge-1", a)
	s.Proxies().Register("edge-2", b)

	s.PushRoutes()

	for _, p := range []*recordingProxy{a, b} {
		select {
		case table := <-p.tables:
			assert.NotNil(t, table)
		case <-time.After(5 * time.Second):
			t.Fatal("a connected proxy was not handed the table")
		}
	}

	// An unregistered proxy stops being pushed to, so a dropped session does
	// not keep a dead agent alive in the fan-out.
	s.Proxies().Unregister("edge-2", b)
	s.PushRoutes()
	select {
	case <-a.tables:
	case <-time.After(5 * time.Second):
		t.Fatal("the remaining proxy was not pushed to")
	}
	select {
	case <-b.tables:
		t.Fatal("an unregistered proxy was still pushed to")
	case <-time.After(100 * time.Millisecond):
	}
}

// The table itself is built from the registry: an app's hostname points at the
// address of the node hosting it, so moving an app moves its route.
func TestRoutesPointAtTheHostingNode(t *testing.T) {
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().EnsureNode("worker-2", "10.111.32.4"))
	app, err := s.apps.CreateApp("blog", &CreateOptions{Host: "worker-2"})
	require.NoError(t, err)

	table, err := s.Routes()
	require.NoError(t, err)
	var target string
	for _, route := range table.Routes {
		if route.Host == "blog."+s.config.BaseDomain {
			target = route.Target
		}
	}
	assert.Equal(t, fmt.Sprintf("10.111.32.4:%d", app.Port), target)
}

// An app's public hostname routes to its port; an active custom domain routes
// to the same target, which is what makes a verified domain start serving.
func TestRoutesCoverPublicHostnamesAndCustomDomains(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{Domain: "www.phil.example", AppName: "blog", Status: store.DomainActive}))

	table, err := s.Routes()
	require.NoError(t, err)
	require.NotZero(t, table.Seq)
	hosts := map[string]string{}
	for _, route := range table.Routes {
		hosts[route.Host] = route.Target
	}
	assert.Equal(t, "127.0.0.1:10000", hosts["blog.apps.example.com"])
	assert.Equal(t, "127.0.0.1:10000", hosts["www.phil.example"], "an active custom domain routes to the same app")

	// The seq moves only when the content does, so a proxy is not handed a new
	// table for every tick of the watch loop.
	again, err := s.Routes()
	require.NoError(t, err)
	assert.Equal(t, table.Seq, again.Seq)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	changed, err := s.Routes()
	require.NoError(t, err)
	assert.Greater(t, changed.Seq, table.Seq, "a change bumps the seq")
}

// The proxy terminates TLS, so it gets real key material -- through the exact
// combined lookup control's own listener uses.
func TestCertForServesPEMThroughTheCombinedLookup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// Without TLS wired (the test default), control says so rather than
	// pretending the name is unknown.
	_, err := s.CertFor("blog.apps.example.com")
	assert.ErrorIs(t, err, errTLSNotManaged)

	ca, err := cluster.NewCA()
	require.NoError(t, err)
	cert, err := ca.Issue("blog.apps.example.com", cluster.RoleNode)
	require.NoError(t, err)
	s.tlsGetCert = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }

	mat, err := s.CertFor("blog.apps.example.com")
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(mat.CertPEM), []byte(mat.KeyPEM))
	require.NoError(t, err, "the PEM material must load as a working key pair")
	require.NotEmpty(t, pair.Certificate)

	// A name control manages no certificate for is a definite answer, so the
	// proxy can tell "not ours" from "cannot reach control".
	s.tlsGetCert = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("nope") }
	_, err = s.CertFor("nobody.example.com")
	assert.ErrorIs(t, err, proxyapi.ErrNoCert)
}

// The routing table's version must keep increasing across a control restart.
// A proxy persists the last table it stored and discards a push whose seq is
// not newer, so a counter that restarts at 1 would leave a proxy holding 1
// from the previous lifetime serving a stale table with no way to notice. The
// counter therefore lives in the registry, like everything else control is
// authoritative for.
func TestRouteTableVersionSurvivesAControlRestart(t *testing.T) {
	t.Parallel()
	conf, st := newProxyTestDeps(t)
	first := New(conf, NewManager(conf, st, testServices(newFakeSystem(), newFakeRunner())), user.NewManager(conf, st))
	t.Cleanup(first.apps.WaitBackground)
	_, err := first.apps.CreateApp("one", nil)
	require.NoError(t, err)
	before, err := first.Routes()
	require.NoError(t, err)
	require.NotZero(t, before.Seq)

	// A new control process over the same registry, with different routes.
	second := New(conf, NewManager(conf, st, testServices(newFakeSystem(), newFakeRunner())), user.NewManager(conf, st))
	t.Cleanup(second.apps.WaitBackground)
	_, err = second.apps.CreateApp("two", nil)
	require.NoError(t, err)
	after, err := second.Routes()
	require.NoError(t, err)

	assert.Greater(t, after.Seq, before.Seq, "a restarted control must not reuse a version the proxy already has")
}

func newProxyTestDeps(t *testing.T) (*controlconf.Config, *store.Store) {
	t.Helper()
	conf := controlconf.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = testToken
	conf.AppsDir, conf.DataDir = t.TempDir(), t.TempDir()
	st, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return conf, st
}

// `proxy list` is where an operator checks a proxy is alive, so its last-seen
// has to mean "answered recently", not "connected at some point". Control asks
// each connected proxy how it is and records the answer.
func TestProxyHeartbeatRecordsLiveness(t *testing.T) {
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().EnsureProxy("edge-1"))
	s.Proxies().Register("edge-1", &recordingProxy{tables: make(chan *proxyapi.Table, 1)})

	before, err := s.apps.Store().Proxy("edge-1")
	require.NoError(t, err)
	require.True(t, before.LastSeen.IsZero(), "nothing has been recorded yet")

	s.proxyHeartbeatPass()

	after, err := s.apps.Store().Proxy("edge-1")
	require.NoError(t, err)
	assert.False(t, after.LastSeen.IsZero(), "a proxy that answered is recorded as seen")
}

// `proxy remove` deletes the registry row from a separate process, so the
// running daemon has to notice: otherwise a removed proxy keeps its session and
// keeps being handed the routing table until it happens to reconnect.
func TestProxyHeartbeatDropsARemovedProxy(t *testing.T) {
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().EnsureProxy("edge-1"))
	agent := &recordingProxy{tables: make(chan *proxyapi.Table, 1)}
	s.Proxies().Register("edge-1", agent)
	require.NoError(t, s.apps.Store().DeleteProxy("edge-1"))

	s.proxyHeartbeatPass()

	assert.NotContains(t, s.Proxies().Agents(), "edge-1", "a removed proxy loses its session")
}
