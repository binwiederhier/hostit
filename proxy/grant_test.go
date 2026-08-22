package proxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/appgrant"
	"heckel.io/hostit/proxyapi"
)

// The point of the whole exercise: a private app is served BY THE PROXY, from
// the table it already holds, so it keeps working when control is down -- the
// same property public apps have always had. The proxy verifies the grant with
// a public key it cannot mint with, and checks the user id against the set
// control resolved for it.
func TestPrivateAppsAreServedByTheProxy(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	// Control is DOWN for this whole test: nothing may depend on it.
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq:            1,
		GrantPublicKey: signer.PublicKey(),
		Admins:         []string{"admin1"},
		Routes: []proxyapi.Route{{
			Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash",
			Private: true, Access: []string{"owner1", "guest1"},
		}},
	}))

	serve := func(grantFor string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://ignored/", nil)
		req.Host = "dash.example.com"
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		if grantFor != "" {
			value, err := signer.Sign("dash", grantFor)
			require.NoError(t, err)
			req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
		}
		p.ServeHTTP(rr, req)
		return rr
	}

	assert.Equal(t, "the app itself", serve("owner1").Body.String(), "the owner, with control down")
	assert.Equal(t, "the app itself", serve("guest1").Body.String(), "somebody granted access")
	assert.Equal(t, "the app itself", serve("admin1").Body.String(), "an admin, from the global set")

	// Everyone else falls through to control, which is down here -- the refusal
	// path is control's to render, and only the ALLOW path had to move.
	assert.NotEqual(t, "the app itself", serve("stranger").Body.String(), "somebody not in the set")
	assert.NotEqual(t, "the app itself", serve("").Body.String(), "no grant at all")
}

// A grant for one app must not open another, and a grant signed by anyone else
// must not open anything. The proxy holds only the public half, so it can say
// no to both without being able to say yes on its own.
func TestTheProxyRefusesGrantsItShouldNot(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	impostor := appgrant.NewSigner("some other key", time.Hour)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash", Private: true, Access: []string{"owner1"}}},
	}))

	for _, tc := range []struct {
		name  string
		grant func() (string, error)
	}{
		{"a grant for another app", func() (string, error) { return signer.Sign("otherapp", "owner1") }},
		{"a grant signed by someone else", func() (string, error) { return impostor.Sign("dash", "owner1") }},
		{"an expired grant", func() (string, error) {
			return appgrant.NewSigner("session-key", -time.Minute).Sign("dash", "owner1")
		}},
	} {
		value, err := tc.grant()
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://ignored/", nil)
		req.Host = "dash.example.com"
		req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
		p.ServeHTTP(rr, req)
		assert.NotEqual(t, "the app itself", rr.Body.String(), tc.name)
	}
}

// The grant cookie is on the app's own hostname, so the browser sends it with
// every request; the app must still never see it.
func TestTheProxyStripsTheGrantBeforeForwarding(t *testing.T) {
	t.Parallel()
	seen := make(chan *http.Request, 1)
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
	}))
	defer appSrv.Close()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash", Private: true, Access: []string{"owner1"}}},
	}))

	value, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "dash.example.com"
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
	req.AddCookie(&http.Cookie{Name: "app_own_cookie", Value: "keep-me"})
	p.ServeHTTP(httptest.NewRecorder(), req)

	forwarded := <-seen
	_, err = forwarded.Cookie(proxyapi.GrantCookie)
	assert.Error(t, err, "the grant was stripped")
	own, err := forwarded.Cookie("app_own_cookie")
	require.NoError(t, err)
	assert.Equal(t, "keep-me", own.Value, "the app's own cookies still arrive")
}

// A request carrying a bearer token is control's to judge: token hashes are not
// in the table, so the proxy has nothing to check it against and must not guess.
func TestBearerTokensStillGoToControl(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "control decided")
	}))
	defer controlSrv.Close()
	p := New(&Config{ControlURL: controlSrv.URL, CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash", Private: true, Access: []string{"owner1"}}},
	}))

	value, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "dash.example.com"
	req.Header.Set("Authorization", "Bearer sometoken")
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
	p.ServeHTTP(rr, req)
	assert.Equal(t, "control decided", rr.Body.String())
}

// A grant names an app, and apps can be renamed. The old name must stop
// working rather than keep opening whatever now answers on that route.
func TestARenamedAppInvalidatesOldGrants(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	oldGrant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)

	// Control pushes the table again after the rename: same app, new name.
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "board.example.com", Target: appSrv.Listener.Addr().String(), App: "board", Private: true, Access: []string{"owner1"}}},
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "board.example.com"
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: oldGrant})
	p.ServeHTTP(rr, req)
	assert.NotEqual(t, "the app itself", rr.Body.String(), "the grant for the old name buys nothing")
}

// The table can arrive without a grant key (an older control, or before the
// first push landed). Failing closed matters more than serving.
func TestNoGrantKeyMeansNoPrivateServing(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	value, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq:    1, // no GrantPublicKey
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash", Private: true, Access: []string{"owner1"}}},
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "dash.example.com"
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
	p.ServeHTTP(rr, req)
	assert.NotEqual(t, "the app itself", rr.Body.String(), "without a key to check with, nothing is served")
}

// A private route with an empty access set must serve nobody, rather than
// treating "no restrictions listed" as "no restrictions".
func TestAnEmptyAccessSetServesNobody(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	signer := appgrant.NewSigner("session-key", time.Hour)
	value, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), App: "dash", Private: true}},
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "dash.example.com"
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: value})
	p.ServeHTTP(rr, req)
	assert.NotEqual(t, "the app itself", rr.Body.String())
}

// A private app with nothing to prove access falls through to control, which
// owns the refusal, the sign-in bounce and the error page.
func TestPrivateRoutesAreHandedToControl(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	defer appSrv.Close()
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "control saw host=%s", r.Host)
	}))
	defer controlSrv.Close()

	p := New(&Config{ControlURL: controlSrv.URL, CacheDir: t.TempDir()})
	require.NoError(t, p.ApplyRoutes(table(1,
		proxyapi.Route{Host: "blog.example.com", Target: appSrv.Listener.Addr().String()},
		proxyapi.Route{Host: "dash.example.com", Target: appSrv.Listener.Addr().String(), Private: true},
	)))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "dash.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "control saw host=dash.example.com", rr.Body.String(), "a private app is gated by control")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "blog.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "the app itself", rr.Body.String(), "a public app still goes straight to its target")
}

// privateProxy is a proxy serving one private app, with control unreachable --
// which is the state these tests are actually about.
func privateProxy(t *testing.T, cacheDir string, routes ...proxyapi.Route) (*Proxy, *appgrant.Signer, string) {
	t.Helper()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the app itself")
	}))
	t.Cleanup(appSrv.Close)
	signer := appgrant.NewSigner("session-key", time.Hour)
	for i := range routes {
		routes[i].Target = appSrv.Listener.Addr().String()
	}
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: cacheDir})
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{Seq: 1, GrantPublicKey: signer.PublicKey(), Routes: routes}))
	return p, signer, appSrv.Listener.Addr().String()
}

func getAs(t *testing.T, p *Proxy, host, path, cookieName, grant string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "http://ignored"+path, nil)
	req.Host = host
	if grant != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: grant})
	}
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	return rr
}

// hostit's own endpoints on a private app's hostname belong to control even
// when the visitor is allowed in. Forwarding /hostit/logout to the app would
// 404 there and leave the grant cookie in place -- sign-out broken in exactly
// the case it is used.
func TestHostitPathsGoToControlEvenWithAValidGrant(t *testing.T) {
	t.Parallel()
	p, signer, _ := privateProxy(t, t.TempDir(), proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1"}})
	grant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)

	for _, path := range []string{proxyapi.LogoutPath, proxyapi.AuthPath, proxyapi.GrantedPath} {
		rr := getAs(t, p, "dash.example.com", path, proxyapi.GrantCookie, grant)
		assert.NotEqual(t, "the app itself", rr.Body.String(), path+" must not reach the app")
	}
	assert.Equal(t, "the app itself", getAs(t, p, "dash.example.com", "/", proxyapi.GrantCookie, grant).Body.String(),
		"ordinary paths are unaffected")
}

// The headline claim of serving private apps from the proxy is that it survives
// a restart while control is down -- which only holds if the grant key and the
// access sets make the round trip through routes.json.
func TestAPrivateAppSurvivesAProxyRestartFromDisk(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	_, signer, target := privateProxy(t, cache, proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1"}})
	grant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)
	stranger, err := signer.Sign("dash", "nobody")
	require.NoError(t, err)

	// A brand-new proxy over the same cache, control still unreachable.
	cold := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: cache})
	require.Equal(t, int64(1), cold.Seq(), "it came back with the persisted table")
	require.Equal(t, target, cold.table.Load().(*proxyapi.Table).Routes[0].Target)

	assert.Equal(t, "the app itself", getAs(t, cold, "dash.example.com", "/", proxyapi.GrantCookie, grant).Body.String())
	assert.NotEqual(t, "the app itself", getAs(t, cold, "dash.example.com", "/", proxyapi.GrantCookie, stranger).Body.String())
}

// Revoking somebody is a new table, and the proxy must act on it.
func TestANewTableRevokesAccess(t *testing.T) {
	t.Parallel()
	p, signer, target := privateProxy(t, t.TempDir(), proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1", "guest1"}})
	grant, err := signer.Sign("dash", "guest1")
	require.NoError(t, err)
	require.Equal(t, "the app itself", getAs(t, p, "dash.example.com", "/", proxyapi.GrantCookie, grant).Body.String())

	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 2, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "dash.example.com", Target: target, App: "dash", Private: true, Access: []string{"owner1"}}},
	}))
	assert.NotEqual(t, "the app itself", getAs(t, p, "dash.example.com", "/", proxyapi.GrantCookie, grant).Body.String(),
		"the same still-valid grant no longer buys access")
}

// A custom domain says nothing about which app it serves, which is why the
// grant is matched against the route's app rather than its hostname.
func TestAPrivateCustomDomainIsServedToo(t *testing.T) {
	t.Parallel()
	p, signer, _ := privateProxy(t, t.TempDir(),
		proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1"}},
		proxyapi.Route{Host: "dash.example.org", App: "dash", Private: true, Access: []string{"owner1"}},
	)
	grant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)

	assert.Equal(t, "the app itself", getAs(t, p, "dash.example.org", "/", proxyapi.GrantCookie, grant).Body.String(),
		"the grant names the app, and the custom domain is that app")
}

// Control sets the __Host- prefixed name wherever the browser accepts it, so
// that is the name most real requests carry.
func TestTheHostPrefixedCookieNameIsAccepted(t *testing.T) {
	t.Parallel()
	p, signer, _ := privateProxy(t, t.TempDir(), proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1"}})
	grant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)

	assert.Equal(t, "the app itself", getAs(t, p, "dash.example.com", "/", proxyapi.GrantCookieHost, grant).Body.String())
}

// An empty user id is what an ownerless app and a token-minted grant both carry.
// Matching one against the other would be an accident, not a decision.
func TestAnEmptyUserIdMatchesNothing(t *testing.T) {
	t.Parallel()
	p, signer, _ := privateProxy(t, t.TempDir(), proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{""}})
	grant, err := signer.Sign("dash", "")
	require.NoError(t, err)

	assert.NotEqual(t, "the app itself", getAs(t, p, "dash.example.com", "/", proxyapi.GrantCookie, grant).Body.String())
}

// The grant must never reach the app, including on a route that is public NOW.
// Making an app public does not expire anybody's cookie, so a formerly private
// app would otherwise start receiving its visitors' credentials.
func TestTheGrantIsStrippedFromPublicRoutesToo(t *testing.T) {
	t.Parallel()
	seen := make(chan *http.Request, 1)
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
	}))
	defer appSrv.Close()
	signer := appgrant.NewSigner("session-key", time.Hour)
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	require.NoError(t, p.ApplyRoutes(&proxyapi.Table{
		Seq: 1, GrantPublicKey: signer.PublicKey(),
		Routes: []proxyapi.Route{{Host: "pub.example.com", Target: appSrv.Listener.Addr().String(), App: "pub"}},
	}))

	grant, err := signer.Sign("pub", "u1")
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "pub.example.com"
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookieHost, Value: grant})
	req.AddCookie(&http.Cookie{Name: "app_own_cookie", Value: "keep-me"})
	p.ServeHTTP(httptest.NewRecorder(), req)

	forwarded := <-seen
	_, err = forwarded.Cookie(proxyapi.GrantCookieHost)
	assert.Error(t, err, "a public app does not get to see grants either")
	own, err := forwarded.Cookie("app_own_cookie")
	require.NoError(t, err)
	assert.Equal(t, "keep-me", own.Value)
}

// Apps are subdomains of the web hostname, so any tenant can set a bare
// `hostit_app` cookie with a Domain that reaches every other app. That is the
// exact thing the __Host- prefix exists to prevent, so under TLS only the
// prefixed name counts.
func TestOnlyTheHostPrefixedNameCountsUnderTLS(t *testing.T) {
	t.Parallel()
	p, signer, _ := privateProxy(t, t.TempDir(), proxyapi.Route{Host: "dash.example.com", App: "dash", Private: true, Access: []string{"owner1"}})
	grant, err := signer.Sign("dash", "owner1")
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "https://dash.example.com/", nil)
	req.Host = "dash.example.com"
	req.TLS = &tls.ConnectionState{}
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookie, Value: grant}) // bare name
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	assert.NotEqual(t, "the app itself", rr.Body.String(), "a bare-named cookie is not a grant over TLS")

	req = httptest.NewRequest("GET", "https://dash.example.com/", nil)
	req.Host = "dash.example.com"
	req.TLS = &tls.ConnectionState{}
	req.AddCookie(&http.Cookie{Name: proxyapi.GrantCookieHost, Value: grant})
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	assert.Equal(t, "the app itself", rr.Body.String())
}
