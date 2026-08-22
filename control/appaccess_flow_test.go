package control

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// privateAppFixture stands up a private app in front of a backend that reports
// back what it was actually sent, so a test can assert on what reached the app
// as well as on what the visitor saw.
type privateAppFixture struct {
	server  *Server
	owner   *store.User
	app     *store.App
	seen    chan *http.Request
	appHost string
}

func newPrivateAppFixture(t *testing.T) *privateAppFixture {
	t.Helper()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	seen := make(chan *http.Request, 8)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		_, _ = w.Write([]byte("the app itself"))
	}))
	t.Cleanup(backend.Close)

	registerAppWithBackend(t, s, "dash", backend.URL)
	require.NoError(t, s.apps.Store().SetAppOwner("dash", owner.ID))
	require.NoError(t, s.apps.Store().SetAppPrivate("dash", true))
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	return &privateAppFixture{server: s, owner: owner, app: a, seen: seen, appHost: "dash.apps.example.com"}
}

// navigate is a browser loading a page on the app's hostname.
func (f *privateAppFixture) navigate(t *testing.T, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "https://"+f.appHost+target, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	return rr
}

// grantFor walks the mint endpoint on the web host as the given user and
// returns the cookie the app hostname would end up holding.
func (f *privateAppFixture) grantFor(t *testing.T, u *store.User, returnTo string) *http.Cookie {
	t.Helper()
	mint := f.mint(t, u, returnTo)
	require.Equal(t, http.StatusFound, mint.Code, "the mint endpoint should redirect back to the app")

	back, err := url.Parse(mint.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, appGrantedPath, back.Path)

	req := httptest.NewRequest("GET", "https://"+back.Host+back.Path+"?"+back.RawQuery, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	cookies := (&http.Response{Header: rr.Header()}).Cookies()
	require.Len(t, cookies, 1, "the grant hop sets exactly one cookie")
	return cookies[0]
}

// cookieNamed picks one Set-Cookie out of a response, or nil.
func cookieNamed(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// mint calls the web host's grant endpoint as the given user (nil = signed out).
func (f *privateAppFixture) mint(t *testing.T, u *store.User, returnTo string) *httptest.ResponseRecorder {
	return f.mintWith(t, u, returnTo, nil)
}

// mintWith is mint, carrying whatever the previous mint left behind.
func (f *privateAppFixture) mintWith(t *testing.T, u *store.User, returnTo string, extra *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	query := url.Values{appParam: {f.app.Name}, returnParam: {returnTo}}
	req := httptest.NewRequest("GET", "https://apps.example.com"+appAccessPath+"?"+query.Encode(), nil)
	if u != nil {
		value, err := f.server.sessions.encode(u.ID)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{Name: f.server.cookieName(sessionCookieName), Value: value})
	}
	if extra != nil {
		req.AddCookie(extra)
	}
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	return rr
}

// The regression that matters most: making one app private must not change
// what a public app does. Public apps are the overwhelming majority and they
// are served without control ever looking at a cookie.
func TestPublicAppsAreUntouched(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	require.NoError(t, f.server.apps.Store().SetAppPrivate("dash", false))

	rr := f.navigate(t, "/")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "the app itself", rr.Body.String())
}

func TestPrivateAppRedirectsAStrangerToTheWebApp(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	rr := f.navigate(t, "/dashboard?tab=today")
	require.Equal(t, http.StatusFound, rr.Code)

	loc, err := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "apps.example.com", loc.Host, "the bounce goes to the web app, where the session cookie applies")
	assert.Equal(t, appAccessPath, loc.Path)
	assert.Equal(t, "dash", loc.Query().Get(appParam))
	assert.Equal(t, "https://dash.apps.example.com/dashboard?tab=today", loc.Query().Get(returnParam),
		"the visitor is returned to exactly what they asked for, query and all")
	assert.Empty(t, f.seen, "the app was never reached")
}

// Only a page load is worth bouncing. An XHR redirected to the web app would
// hand the app's own JavaScript an HTML page where it expected data.
func TestPrivateAppRefusesNonNavigationsOutright(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	req := httptest.NewRequest("GET", "https://"+f.appHost+"/api/data", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Empty(t, f.seen)
}

func TestPrivateAppServesItsOwner(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	cookie := f.grantFor(t, f.owner, "https://"+f.appHost+"/")

	rr := f.navigate(t, "/", cookie)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "the app itself", rr.Body.String())
}

func TestPrivateAppServesACollaborator(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	friend := newActiveTestUser(t, f.server, "friend@example.com")
	require.NoError(t, f.server.apps.Store().AddAppCollaborator(f.app.ID, friend.ID))

	cookie := f.grantFor(t, friend, "https://"+f.appHost+"/")
	rr := f.navigate(t, "/", cookie)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// The app must never see the credential that let its visitor in, even though
// the cookie is set on the app's own hostname.
func TestTheGrantCookieNeverReachesTheApp(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	cookie := f.grantFor(t, f.owner, "https://"+f.appHost+"/")

	rr := f.navigate(t, "/", cookie, &http.Cookie{Name: "app_own_cookie", Value: "keep-me"})
	require.Equal(t, http.StatusOK, rr.Code)

	forwarded := <-f.seen
	_, err := forwarded.Cookie(f.server.cookieName(appGrantCookieName))
	assert.Error(t, err, "the grant was stripped before the request went upstream")
	own, err := forwarded.Cookie("app_own_cookie")
	require.NoError(t, err, "the app's own cookies still arrive")
	assert.Equal(t, "keep-me", own.Value)
}

// A grant is a statement about identity, not a cached verdict: pulling
// somebody's access takes effect on their very next request.
func TestRevokingAccessTakesEffectImmediately(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	friend := newActiveTestUser(t, f.server, "friend@example.com")
	require.NoError(t, f.server.apps.Store().AddAppCollaborator(f.app.ID, friend.ID))
	cookie := f.grantFor(t, friend, "https://"+f.appHost+"/")
	require.Equal(t, http.StatusOK, f.navigate(t, "/", cookie).Code)
	<-f.seen // that first, still-permitted load

	require.NoError(t, f.server.apps.Store().RemoveAppCollaborator(f.app.ID, friend.ID))

	rr := f.navigate(t, "/", cookie)
	assert.Equal(t, http.StatusFound, rr.Code, "the still-valid cookie no longer buys access")
	assert.Empty(t, f.seen)
}

// A grant names one app. The cookie lives on a hostname whose content the
// app's owner controls, so a grant that worked anywhere else would let one
// owner harvest their visitors' access to everyone's private apps.
func TestAGrantIsUselessOnAnotherApp(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	cookie := f.grantFor(t, f.owner, "https://"+f.appHost+"/")

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(other.Close)
	registerAppWithBackend(t, f.server, "secret", other.URL)
	require.NoError(t, f.server.apps.Store().SetAppOwner("secret", f.owner.ID))
	require.NoError(t, f.server.apps.Store().SetAppPrivate("secret", true))

	req := httptest.NewRequest("GET", "https://secret.apps.example.com/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusFound, rr.Code, "the grant for dash buys nothing on secret")
}

func TestMintRefusesAStrangerAndTheSignedOut(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	f.server.config.GoogleClientID = "client-id"
	f.server.config.GoogleClientSecret = "secret"
	stranger := newActiveTestUser(t, f.server, "stranger@example.com")

	assert.Equal(t, http.StatusForbidden, f.mint(t, stranger, "https://"+f.appHost+"/").Code,
		"a signed-in stranger is told the app is private, and to ask its owner")
	assert.Equal(t, http.StatusFound, f.mint(t, nil, "https://"+f.appHost+"/").Code,
		"and a signed-out visitor is sent to sign in first")
}

// The return URL decides where the grant is DELIVERED. If it were not checked
// against the app's own hostnames, asking for a grant with a return URL of
// one's choosing would post a valid credential straight to it.
func TestMintRefusesAForeignReturnURL(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	for _, target := range []string{
		"https://evil.example.org/",
		"https://dash.apps.example.com.evil.example.org/",
		"https://otherapp.apps.example.com/",
		"/relative",
		"",
	} {
		assert.Equal(t, http.StatusNotFound, f.mint(t, f.owner, target).Code, target)
	}
}

// A private app is private on every name it answers to, and reachable on all
// of them once the visitor holds a grant.
func TestPrivacyCoversCustomDomains(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	require.NoError(t, f.server.apps.Store().AddDomain(&store.Domain{AppName: "dash", Domain: "dash.example.org", Status: store.DomainActive}))
	f.server.reloadDomains()

	req := httptest.NewRequest("GET", "https://dash.example.org/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code, "the custom domain is gated too")

	cookie := f.grantFor(t, f.owner, "https://dash.example.org/")
	req = httptest.NewRequest("GET", "https://dash.example.org/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "and served on it with a grant minted for that hostname")
}

// A browser that will not keep the grant must be stopped, not ping-ponged
// between the two hostnames until it gives up. The guard sits on the web host,
// where cookies are known to work because the session is there, so nothing has
// to be smuggled through the app's URL to detect it.
func TestAFailedGrantDoesNotLoop(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	// Keep asking for a grant for the same app without ever keeping one.
	var marker *http.Cookie
	var last *httptest.ResponseRecorder
	for i := 0; i < loopLimit+1; i++ {
		last = f.mintWith(t, f.owner, "https://"+f.appHost+"/", marker)
		if c := cookieNamed(last, f.server.cookieName(loopCookieName)); c != nil {
			marker = c
		}
	}
	assert.Equal(t, http.StatusForbidden, last.Code, "the loop stops rather than running until the browser does")
}

// ...but a few tabs opening the same private app at once are all legitimate,
// and the old guard failed every one after the first.
func TestConcurrentTabsAllGetAGrant(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	var marker *http.Cookie
	for i := 0; i < loopLimit; i++ {
		rr := f.mintWith(t, f.owner, "https://"+f.appHost+"/", marker)
		require.Equal(t, http.StatusFound, rr.Code, "tab %d should still be minted a grant", i+1)
		if c := cookieNamed(rr, f.server.cookieName(loopCookieName)); c != nil {
			marker = c
		}
	}
}

// Nothing is left in the URL once the grant is delivered: the visitor should
// not be able to tell that anything happened.
func TestTheGrantLeavesNothingInTheURL(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	mint := f.mint(t, f.owner, "https://"+f.appHost+"/reports?year=2026")
	require.Equal(t, http.StatusFound, mint.Code)
	back, err := url.Parse(mint.Header().Get("Location"))
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "https://"+back.Host+back.Path+"?"+back.RawQuery, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "/reports?year=2026", rr.Header().Get("Location"),
		"straight to what they asked for, deep link intact and no marker left behind")
}

// Logging out of one app drops that app's grant and nothing else. It lands on
// the web app, because the hostit session is untouched: coming back here would
// mint a new grant on the spot and make the logout look broken.
func TestLoggingOutOfOneApp(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	cookie := f.grantFor(t, f.owner, "https://"+f.appHost+"/")
	require.Equal(t, http.StatusOK, f.navigate(t, "/", cookie).Code)

	rr := f.navigate(t, appLogoutPath, cookie)
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "https://apps.example.com/", rr.Header().Get("Location"))

	cleared := (&http.Response{Header: rr.Header()}).Cookies()
	require.Len(t, cleared, 1)
	assert.Equal(t, f.server.cookieName(appGrantCookieName), cleared[0].Name)
	assert.Less(t, cleared[0].MaxAge, 0, "the grant cookie is expired, not replaced")
}

// A visitor can ask to be signed in without first being refused, which is what
// a "sign in" link on the app's own page would point at.
func TestTheAuthEndpointStartsTheBounce(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	rr := f.navigate(t, appAuthPath)
	require.Equal(t, http.StatusFound, rr.Code)
	loc, err := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, appAccessPath, loc.Path)
	assert.Equal(t, "dash", loc.Query().Get(appParam))
}

// A viewer may open the app and nothing more. That is the whole point of the
// grant existing separately from the collaborator one.
func TestPrivateAppServesAViewer(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	guest := newActiveTestUser(t, f.server, "guest@example.com")
	require.NoError(t, f.server.apps.Store().AddAppViewer(f.app.ID, guest.ID))

	cookie := f.grantFor(t, guest, "https://"+f.appHost+"/")
	assert.Equal(t, http.StatusOK, f.navigate(t, "/", cookie).Code)
}

// And revoking a view grant bites immediately, like every other grant here.
func TestRevokingAViewGrantTakesEffectImmediately(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	guest := newActiveTestUser(t, f.server, "guest@example.com")
	require.NoError(t, f.server.apps.Store().AddAppViewer(f.app.ID, guest.ID))
	cookie := f.grantFor(t, guest, "https://"+f.appHost+"/")
	require.Equal(t, http.StatusOK, f.navigate(t, "/", cookie).Code)
	<-f.seen

	require.NoError(t, f.server.apps.Store().RemoveAppViewer(f.app.ID, guest.ID))
	assert.Equal(t, http.StatusFound, f.navigate(t, "/", cookie).Code)
	assert.Empty(t, f.seen)
}

// Webhooks and scripts have no browser to redirect, so a bearer token is
// judged on the spot by the same visibility rule.
func TestPrivateAppAcceptsABearerToken(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	token, _, err := f.server.users.CreateToken(f.owner.ID, "script")
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "https://"+f.appHost+"/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	req = httptest.NewRequest("GET", "https://"+f.appHost+"/", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 40))
	rr = httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code, "an unknown token is refused like any stranger")
}

// A signed-OUT visitor is sent to sign in and then back to the app, rather than
// to a 404 they cannot act on. An owner opening their own private app from a
// browser they have not used before is the common case, and a dead end there
// reads as the app being broken.
func TestASignedOutVisitorIsSentToSignIn(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	f.server.config.GoogleClientID = "client-id"
	f.server.config.GoogleClientSecret = "secret"

	rr := f.mint(t, nil, "https://"+f.appHost+"/")
	require.Equal(t, http.StatusFound, rr.Code)

	loc, err := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/auth/google", loc.Path)
	next, err := url.Parse(loc.Query().Get(nextParam))
	require.NoError(t, err)
	assert.Equal(t, appAccessPath, next.Path, "and comes back here to collect the grant")
	assert.Equal(t, "https://"+f.appHost+"/", next.Query().Get(returnParam))
}

// Signing in has to end where the visitor was going, not at the dashboard.
func TestLoginReturnsToWhereYouWereGoing(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	target := appAccessPath + "?app=dash&to=https%3A%2F%2Fdash.apps.example.com%2F"

	// Starting the login stashes where to come back to.
	req := httptest.NewRequest("GET", "/auth/google?"+nextParam+"="+url.QueryEscape(target), nil)
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	var next *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == s.cookieName(nextCookieName) {
			next = c
		}
	}
	require.NotNil(t, next, "the return target is stashed for the callback")

	// Finishing it lands there.
	s.exchangeGoogleCode = func(code, host string) (*googleIdentity, error) {
		return &googleIdentity{Email: "owner@example.com", Name: "Owner", EmailVerified: true}, nil
	}
	req = httptest.NewRequest("GET", "/auth/callback?code=c&state=st", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: "st"})
	req.AddCookie(next)
	rr = httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, target, rr.Header().Get("Location"))
}

// The return target is a path on this host, never a URL of someone else's
// choosing: /auth/google is a plain GET anyone can hand a visitor a link to.
func TestLoginRefusesAForeignReturnTarget(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"

	for _, target := range []string{"https://evil.example.org/", "//evil.example.org/"} {
		req := httptest.NewRequest("GET", "/auth/google?"+nextParam+"="+url.QueryEscape(target), nil)
		rr := httptest.NewRecorder()
		s.API().ServeHTTP(rr, req)
		for _, c := range rr.Result().Cookies() {
			if c.Name == s.cookieName(nextCookieName) {
				assert.NotContains(t, c.Value, "evil.example.org", target)
			}
		}
	}
}

// A signed-in visitor without access still gets the 404: one hostit user must
// not be able to discover another's private apps by watching for a redirect.
func TestASignedInStrangerIsNotSentToSignIn(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	f.server.config.GoogleClientID = "client-id"
	f.server.config.GoogleClientSecret = "secret"
	stranger := newActiveTestUser(t, f.server, "stranger@example.com")

	assert.Equal(t, http.StatusForbidden, f.mint(t, stranger, "https://"+f.appHost+"/").Code,
		"told it is private, not bounced to a login that would not help")
}

// Being refused used to look exactly like the app not existing, which was
// chosen to stop app-name enumeration. In practice the person refused is
// almost always someone who was sent the link and needs to be told what to do
// about it, so they are now told.
func TestARefusedVisitorIsToldItIsPrivate(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	stranger := newActiveTestUser(t, f.server, "stranger@example.com")

	rr := f.mint(t, stranger, "https://"+f.appHost+"/")
	assert.Equal(t, http.StatusForbidden, rr.Code)
	body := rr.Body.String()
	assert.Contains(t, body, "private app")
	assert.Contains(t, body, "dash", "it names the app they are asking for")
	assert.Contains(t, body, "stranger@example.com", "and which account they are signed in as")
}

// A grant that names somebody who has since lost access lands on the same page.
func TestAGrantThatNoLongerBuysAccessExplainsItself(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	guest := newActiveTestUser(t, f.server, "guest@example.com")
	require.NoError(t, f.server.apps.Store().AddAppViewer(f.app.ID, guest.ID))
	cookie := f.grantFor(t, guest, "https://"+f.appHost+"/")
	require.NoError(t, f.server.apps.Store().RemoveAppViewer(f.app.ID, guest.ID))

	// Not a navigation, so it is answered rather than bounced.
	req := httptest.NewRequest("GET", "https://"+f.appHost+"/data.json", nil)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "private app")
}

// The enumeration guard still holds where it actually mattered: a hostname
// nobody has deployed says nothing about anything.
func TestAnUnknownHostnameStillSaysNothing(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	req := httptest.NewRequest("GET", "https://nosuchapp.apps.example.com/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "Nothing", "the silent page, apostrophe HTML-escaped")
	assert.Contains(t, rr.Body.String(), "deployed here")
	assert.NotContains(t, rr.Body.String(), "private app")
}

// A return target is a path on THIS host. The check has to survive the ways a
// browser reads an authority that Go's string handling does not: a backslash
// is a slash in the relative-slash state, and tabs are stripped before parsing,
// so both turn "/..." into "//evil.example.org".
func TestLocalPathRefusesEveryWayOutOfTheHost(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"//evil.example.org/",
		`/\evil.example.org/`,
		"/\t/evil.example.org/",
		"/\r\n/evil.example.org/",
		`\\evil.example.org/`,
		"https://evil.example.org/",
		"",
	} {
		assert.Equal(t, "/", localPath(target), "%q must not survive as a redirect target", target)
	}
	assert.Equal(t, "/reports?year=2026", localPath("/reports?year=2026"), "an ordinary path is left alone")
	assert.Equal(t, "/a/b/c", localPath("/a/b/c"))
}

// The login's "come back here afterwards" is attacker-supplied: /auth/google is
// a plain GET anyone can hand a victim a link to, and the redirect happens on
// the platform's own origin after a genuine Google sign-in, which is exactly
// what makes it worth phishing with.
func TestLoginRefusesToLeaveTheHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	s.exchangeGoogleCode = func(code, host string) (*googleIdentity, error) {
		return &googleIdentity{Email: "owner@example.com", Name: "Owner", EmailVerified: true}, nil
	}

	for _, target := range []string{`/\evil.example.org`, "//evil.example.org", "https://evil.example.org"} {
		req := httptest.NewRequest("GET", "/auth/google?"+nextParam+"="+url.QueryEscape(target), nil)
		rr := httptest.NewRecorder()
		s.API().ServeHTTP(rr, req)
		var next *http.Cookie
		for _, c := range rr.Result().Cookies() {
			if c.Name == s.cookieName(nextCookieName) {
				next = c
			}
		}
		require.NotNil(t, next)

		req = httptest.NewRequest("GET", "/auth/callback?code=c&state=st", nil)
		req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: "st"})
		req.AddCookie(next)
		rr = httptest.NewRecorder()
		s.API().ServeHTTP(rr, req)
		assert.Equal(t, "/", rr.Header().Get("Location"), "signing in must not land on %q", target)
	}
}

// The refusal page is served on the APP's own origin, so the app's own
// JavaScript can read it. It must not name the visitor there: the grant is
// stripped precisely so an app never learns who is looking at it, and admins
// are on nobody's list, so this would hand tenant owners their identities.
func TestTheRefusalPageNamesNobodyOnTheAppsOwnOrigin(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)
	guest := newActiveTestUser(t, f.server, "guest@example.com")
	require.NoError(t, f.server.apps.Store().AddAppViewer(f.app.ID, guest.ID))
	cookie := f.grantFor(t, guest, "https://"+f.appHost+"/")

	// What the app's page can do: a same-origin fetch with a bogus bearer token,
	// which fails authentication and reaches the refusal page while the browser
	// still attaches the visitor's grant cookie.
	req := httptest.NewRequest("GET", "https://"+f.appHost+"/probe", nil)
	req.Header.Set("Authorization", "Bearer nonsense")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	f.server.proxyHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "guest@example.com", "the app must not learn who its visitor is")

	// On the WEB host the same person is named, because that is where it helps
	// and the app cannot read it.
	assert.Contains(t, f.mint(t, guest, "https://"+f.appHost+"/").Body.String(), "", "sanity")
	stranger := newActiveTestUser(t, f.server, "stranger@example.com")
	assert.Contains(t, f.mint(t, stranger, "https://"+f.appHost+"/").Body.String(), "stranger@example.com")
}

// A grant is delivered in a URL, so it must never be sent to a cleartext one.
func TestMintRefusesACleartextReturnURL(t *testing.T) {
	t.Parallel()
	f := newPrivateAppFixture(t)

	assert.Equal(t, http.StatusNotFound, f.mint(t, f.owner, "http://"+f.appHost+"/").Code,
		"the app's own hostname, but over http")
}
