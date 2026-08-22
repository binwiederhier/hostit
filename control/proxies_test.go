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

	"heckel.io/hostit/appgrant"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/node"
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
	require.NoError(t, s.apps.Store().EnsureNode("worker-2", "10.0.0.2"))
	// Registered AND connected: control provisions through the node that hosts
	// the app, so an app cannot be placed on a node that is not there.
	s.apps.NodeRegistry().Register("worker-2", s.apps.testMachine())
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
	assert.Equal(t, fmt.Sprintf("10.0.0.2:%d", app.Port), target)
}

// An app's public hostname routes to its port; an active custom domain routes
// to the same target, which is what makes a verified domain start serving.
func TestRoutesCoverPublicHostnamesAndCustomDomains(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
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

	// An app can hold several active custom domains (e.g. a redirect app that
	// answers for every legacy hostname); every one of them routes directly.
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{Domain: "blog.phil.example", AppName: "blog", Status: store.DomainActive}))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{Domain: "old.phil.example", AppName: "blog", Status: store.DomainActive}))
	table, err = s.Routes()
	require.NoError(t, err)
	hosts = map[string]string{}
	for _, route := range table.Routes {
		hosts[route.Host] = route.Target
	}
	assert.Equal(t, "127.0.0.1:10000", hosts["blog.phil.example"], "second custom domain routes directly too")
	assert.Equal(t, "127.0.0.1:10000", hosts["old.phil.example"], "third custom domain routes directly too")

	// The seq moves only when the content does, so a proxy is not handed a new
	// table for every tick of the watch loop.
	again, err := s.Routes()
	require.NoError(t, err)
	assert.Equal(t, table.Seq, again.Seq)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
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
	first := New(conf, newWiredManager(t, conf, st, testServices(newFakeSystem(), newFakeRunner())), user.NewManager(conf, st))
	t.Cleanup(first.apps.WaitBackground)
	_, err := first.apps.CreateApp("one", nil)
	require.NoError(t, err)
	before, err := first.Routes()
	require.NoError(t, err)
	require.NotZero(t, before.Seq)

	// A new control process over the same registry, with different routes.
	second := New(conf, newWiredManager(t, conf, st, testServices(newFakeSystem(), newFakeRunner())), user.NewManager(conf, st))
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

// What control says an app's disk cap is has to be what the node enforces.
// Nothing is unlimited: the node substitutes a default cap for an unset limit,
// which meant the API and the dashboard reported "no limit" while the
// filesystem was hard-capping the app at 2 GB. Control resolves the effective
// cap instead, so the number an owner sees is the number they get.
func TestUnsetDiskLimitResolvesToTheEnforcedCap(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()

	// A zero limit means "no limit anyone chose", and the node caps it anyway --
	// so control must report the cap rather than the zero.
	defaults, err := s.users.Defaults()
	require.NoError(t, err)
	defaults.DiskMB = 0
	require.NoError(t, s.users.SetDefaults(defaults))

	_, diskMB, _ := s.appLimits("blog")
	assert.Equal(t, node.DefaultDiskCapMB, diskMB, "a zero limit reports the cap the node actually enforces")

	desiredState, err := s.apps.DesiredState("")
	require.NoError(t, err)
	require.Len(t, desiredState.Apps, 1)
	assert.Equal(t, node.DefaultDiskCapMB, desiredState.Apps[0].DiskMB,
		"and the node is told that number rather than a zero it has to reinterpret")
}

// The flag has to ride the table for BOTH hostname forms, or a private app
// stays wide open on its custom domain -- the one URL its owner is most likely
// to have shared.
func TestRoutesMarkPrivateAppsOnEveryHostname(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a2", Name: "dash", Port: 10001, Host: store.HostLocal, Private: true}))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{AppName: "dash", Domain: "dash.example.org", Status: store.DomainActive}))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{AppName: "blog", Domain: "blog.example.org", Status: store.DomainActive}))

	table, err := s.Routes()
	require.NoError(t, err)
	private := make(map[string]bool, len(table.Routes))
	for _, r := range table.Routes {
		private[r.Host] = r.Private
	}
	assert.Equal(t, map[string]bool{
		"blog.apps.example.com": false,
		"blog.example.org":      false,
		"dash.apps.example.com": true,
		"dash.example.org":      true,
	}, private)
}

// Flipping the switch has to change the table, or the proxy keeps serving the
// app publicly from a cached copy until something else happens to bump the
// sequence -- which is the bug this feature exists to prevent.
func TestMakingAnAppPrivateBumpsTheRoutingTable(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal}))

	before, err := s.Routes()
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().SetAppPrivate("dash", true))
	after, err := s.Routes()
	require.NoError(t, err)

	assert.Greater(t, after.Seq, before.Seq, "the table version moved, so proxies will take the new one")
}

// The proxy enforces private access itself, so the table has to carry who may
// open each private app. Control resolves the policy into a set; the proxy
// only does membership, which is what keeps the rule in one place.
func TestRoutesCarryTheAccessSet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	friend := newActiveTestUser(t, s, "friend@example.com")
	guest := newActiveTestUser(t, s, "guest@example.com")
	suspended := newActiveTestUser(t, s, "gone@example.com")
	suspended.Status = store.StatusDenied
	require.NoError(t, s.users.Update(suspended))
	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))

	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a2", Name: "blog", Port: 10001, Host: store.HostLocal, OwnerID: owner.ID}))
	require.NoError(t, s.apps.Store().AddAppCollaborator("a1", friend.ID))
	require.NoError(t, s.apps.Store().AddAppViewer("a1", guest.ID))
	require.NoError(t, s.apps.Store().AddAppViewer("a1", suspended.ID))

	table, err := s.Routes()
	require.NoError(t, err)
	byHost := make(map[string]proxyapi.Route, len(table.Routes))
	for _, r := range table.Routes {
		byHost[r.Host] = r
	}

	private := byHost["dash.apps.example.com"]
	assert.True(t, private.Private)
	assert.ElementsMatch(t, []string{owner.ID, friend.ID, guest.ID}, private.Access,
		"the owner and both grants, and never a suspended account")
	assert.Empty(t, byHost["blog.apps.example.com"].Access, "a public app needs no set at all")
	assert.Equal(t, []string{admin.ID}, table.Admins, "admins ride once, not once per app")
	assert.Equal(t, s.grants.PublicKey(), table.GrantPublicKey, "and the key to check grants with")
}

// A membership change has to move the table, or the proxy keeps enforcing a
// stale set -- which is the difference between revoking access and appearing to.
func TestAccessChangesBumpTheRoutingTable(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	guest := newActiveTestUser(t, s, "guest@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))

	before, err := s.Routes()
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().AddAppViewer("a1", guest.ID))
	added, err := s.Routes()
	require.NoError(t, err)
	assert.Greater(t, added.Seq, before.Seq, "granting access moves the table")

	require.NoError(t, s.apps.Store().RemoveAppViewer("a1", guest.ID))
	removed, err := s.Routes()
	require.NoError(t, err)
	assert.Greater(t, removed.Seq, added.Seq, "and so does taking it away")
}

// A suspended owner may not open their own app -- mayViewApp refuses them --
// so they must not be in the set the proxy enforces on either, or control and
// the proxy disagree for as long as a grant survives.
func TestASuspendedOwnerLeavesTheAccessSet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))

	before, err := s.Routes()
	require.NoError(t, err)
	require.Contains(t, before.Routes[0].Access, owner.ID)

	owner.Status = store.StatusDenied
	require.NoError(t, s.users.Update(owner))

	after, err := s.Routes()
	require.NoError(t, err)
	assert.NotContains(t, after.Routes[0].Access, owner.ID)
	assert.Greater(t, after.Seq, before.Seq, "suspending somebody moves the table, like any other access change")
}

// An app created with the admin token has no owner. An empty id in the set
// would sit there waiting to match a grant that also carries one.
func TestAnOwnerlessAppContributesNoEmptyId(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, Private: true}))

	table, err := s.Routes()
	require.NoError(t, err)
	assert.NotContains(t, table.Routes[0].Access, "", "no empty id rides along")
}

// A custom domain is the URL an owner actually shared, so it has to carry the
// same access set as the app's own hostname rather than an empty one.
func TestACustomDomainCarriesTheSameAccessSet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	guest := newActiveTestUser(t, s, "guest@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	require.NoError(t, s.apps.Store().AddAppViewer("a1", guest.ID))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{AppName: "dash", Domain: "dash.example.org", Status: store.DomainActive}))

	table, err := s.Routes()
	require.NoError(t, err)
	byHost := make(map[string]proxyapi.Route, len(table.Routes))
	for _, r := range table.Routes {
		byHost[r.Host] = r
	}
	assert.ElementsMatch(t, byHost["dash.apps.example.com"].Access, byHost["dash.example.org"].Access)
	assert.Equal(t, "dash", byHost["dash.example.org"].App, "and names the app, since the hostname does not")
}

// The one thing neither side's own tests can catch: that the key control
// PUBLISHES is the key it SIGNS with. They are built from the same session key,
// but nothing else asserts the two ends meet.
func TestThePublishedKeyVerifiesControlsOwnGrants(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, Private: true}))

	table, err := s.Routes()
	require.NoError(t, err)
	require.NotEmpty(t, table.GrantPublicKey)

	verifier, err := appgrant.NewVerifier(table.GrantPublicKey)
	require.NoError(t, err)
	grant, err := s.grants.Sign("dash", "u1")
	require.NoError(t, err)
	app, userID, err := verifier.Verify(grant)
	require.NoError(t, err)
	assert.Equal(t, "dash", app)
	assert.Equal(t, "u1", userID)
}
