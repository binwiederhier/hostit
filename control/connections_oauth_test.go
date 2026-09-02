package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/connections"
	"heckel.io/hostit/store"
)

// fakeAuthServer is a stand-in OAuth 2.0 authorization server: it issues codes
// at /authorize, trades them for tokens at /token, and rotates the access token
// on every refresh so a test can tell a fresh one from a cached one.
//
// It exists so the consent round trip can be driven end to end -- start,
// redirect, callback, store, refresh, deliver to the app -- without the
// internet and without stubbing out the very code paths worth testing.
type fakeAuthServer struct {
	*httptest.Server
	mu sync.Mutex
	// issued maps an authorization code to the refresh token it becomes.
	issued map[string]string
	// authorizeCalls records the query of every consent request, so a test can
	// assert the provider's own parameters actually arrived.
	authorizeCalls []url.Values
	// refreshes counts token refreshes; nth refresh returns "access-N".
	refreshes int
	// longLived makes it behave like Slack or GitHub: an access token, no
	// refresh token.
	longLived bool
	// denyRefresh makes the next refresh fail the way a revoked grant does.
	denyRefresh bool
	// denyProbe makes the health probe fail the way a revoked long-lived token does.
	denyProbe bool
	// rotateRefresh behaves like Discord: every refresh issues a new refresh
	// token and refuses any older one.
	rotateRefresh bool
	live          string
	// revoked records the tokens presented to /revoke.
	revoked []string
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	f := &fakeAuthServer{issued: map[string]string{}}
	mux := http.NewServeMux()

	// The consent screen. A real one shows a page; this one immediately
	// "approves" by redirecting back with a code, which is the only part the
	// system under test cares about.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authorizeCalls = append(f.authorizeCalls, r.URL.Query())
		code := fmt.Sprintf("code-%d", len(f.authorizeCalls))
		f.issued[code] = fmt.Sprintf("refresh-%d", len(f.authorizeCalls))
		f.mu.Unlock()
		redirect := r.URL.Query().Get("redirect_uri")
		http.Redirect(w, r, fmt.Sprintf("%s?code=%s&state=%s", redirect, code,
			url.QueryEscape(r.URL.Query().Get("state"))), http.StatusFound)
	})

	// A cheap authenticated endpoint the long-lived-token probe hits: 401 when a
	// test revokes it, 200 otherwise.
	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		deny := f.denyProbe
		f.mu.Unlock()
		if deny || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			refresh, ok := f.issued[r.Form.Get("code")]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			body := map[string]any{
				"access_token": "access-0", "expires_in": 3600,
				// A user-token provider reads this instead of access_token, the way
				// Slack returns the person's token under authed_user.
				"authed_user": map[string]any{"access_token": "user-access-0"},
			}
			if !f.longLived {
				body["refresh_token"] = refresh
			}
			_ = json.NewEncoder(w).Encode(body)
		case "refresh_token":
			if f.rotateRefresh {
				if f.live == "" {
					f.live = f.issued["code-1"]
				}
				if r.Form.Get("refresh_token") != f.live {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": "invalid_grant", "error_description": "Refresh token is invalid.",
					})
					return
				}
				f.refreshes++
				f.live = fmt.Sprintf("refresh-rotated-%d", f.refreshes)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  fmt.Sprintf("access-%d", f.refreshes),
					"refresh_token": f.live, "expires_in": 3600,
				})
				return
			}
			if f.denyRefresh {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "invalid_grant", "error_description": "Token has been expired or revoked.",
				})
				return
			}
			f.refreshes++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("access-%d", f.refreshes), "expires_in": 3600,
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unsupported_grant_type"})
		}
	})

	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAuthServer) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func (f *fakeAuthServer) lastAuthorize() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorizeCalls[len(f.authorizeCalls)-1]
}

// registerFakeProvider points a provider at the fake server and gives the
// instance a client for it, so it is actually offered.
func registerFakeProvider(t *testing.T, s *Server, f *fakeAuthServer, name string, longLived bool) {
	t.Helper()
	connections.Register(connections.Provider{
		Name: name, Label: "Fake " + name, Kind: connections.KindOAuth,
		Scopes:         []string{"read"},
		AuthURL:        f.URL + "/authorize",
		TokenURL:       f.URL + "/token",
		AuthParams:     map[string]string{"access_type": "offline"},
		LongLivedToken: longLived,
		ProbeURL:       f.URL + "/probe",
		Help:           "a test provider",
	})
	if s.config.ConnectionClients == nil {
		s.config.ConnectionClients = map[string]config.OAuthClient{}
	}
	s.config.ConnectionClients[name] = config.OAuthClient{ClientID: "test-client", ClientSecret: "test-secret"}
}

// registerFakeHybridProvider registers a HybridToken provider (the GitHub
// class): whether it issues a refresh token depends on f.longLived, so a test
// can exercise both the refreshable (GitHub App) and the probe-only (classic
// OAuth App) variants of the same provider.
func registerFakeHybridProvider(t *testing.T, s *Server, f *fakeAuthServer, name string) {
	t.Helper()
	connections.Register(connections.Provider{
		Name: name, Label: "Fake " + name, Kind: connections.KindOAuth,
		Scopes:      []string{"read"},
		AuthURL:     f.URL + "/authorize",
		TokenURL:    f.URL + "/token",
		AuthParams:  map[string]string{"access_type": "offline"},
		HybridToken: true,
		ProbeURL:    f.URL + "/probe",
		Help:        "a test hybrid provider",
	})
	if s.config.ConnectionClients == nil {
		s.config.ConnectionClients = map[string]config.OAuthClient{}
	}
	s.config.ConnectionClients[name] = config.OAuthClient{ClientID: "test-client", ClientSecret: "test-secret"}
}

// GitHub is a HybridToken provider. When the operator's app issues a refresh
// token (a GitHub App, or an OAuth App with token expiration on), the connection
// must be REFRESHED like any OAuth provider. Treating every github connection as
// long-lived was the bug: the expiring access token was never refreshed and died
// overnight, needing a manual reconnect every morning.
func TestHybridConnectionWithRefreshTokenIsRefreshed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeHybridProvider(t, s, f, "fake-hybrid")
	f.longLived = false // issues a refresh token (GitHub App style)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-hybrid","slug":"ghc"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	// It is refreshed, not probed: denying the refresh flips it to needs-reconnect.
	f.denyRefresh = true
	status, err := s.connections.Verify(context.Background(), u.ID, "ghc")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, status, "a refreshable hybrid connection is refreshed")

	// And it recovers when the refresh works again.
	f.denyRefresh = false
	status, err = s.connections.Verify(context.Background(), u.ID, "ghc")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, status)
}

// The other variant of the same provider: a classic OAuth App issues a permanent
// access token and no refresh token. That connection must be PROBED (like Slack),
// not refreshed -- refreshing a permanent token would fail, and refusing the
// connect (as a pure refreshing provider does) would lock out a valid app.
func TestHybridConnectionWithoutRefreshTokenIsProbed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeHybridProvider(t, s, f, "fake-hybrid-perm")
	f.longLived = true // no refresh token (classic OAuth App style)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-hybrid-perm","slug":"gh2"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	// It is probed, not refreshed: denying the probe flips it to needs-reconnect.
	f.denyProbe = true
	status, err := s.connections.Verify(context.Background(), u.ID, "gh2")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, status, "a permanent-token hybrid connection is probed")

	// And it recovers when the probe answers 200 again.
	f.denyProbe = false
	status, err = s.connections.Verify(context.Background(), u.ID, "gh2")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, status)
}

// signIn gives the test a browser-shaped session cookie, which is what the
// OAuth callback authenticates with.
func signIn(t *testing.T, s *Server, u *store.User) *http.Cookie {
	t.Helper()
	value, err := s.sessions.encode(u.ID)
	require.NoError(t, err)
	return &http.Cookie{Name: s.cookieName(sessionCookieName), Value: value}
}

// browse follows the consent round trip the way a browser does: request the
// consent URL, let the fake server redirect back, and deliver that callback to
// hostit carrying the cookies it set.
func browse(t *testing.T, s *Server, consentURL string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	// Step 1: the provider's consent screen, which redirects back with a code.
	req, err := http.NewRequest("GET", consentURL, nil)
	require.NoError(t, err)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	// Step 2: the callback, on hostit, with the cookies from step 0.
	cb := httptest.NewRequest("GET", "/auth/callback?"+back.RawQuery, nil)
	cb.Host = "hostit.apps.example.com"
	for _, c := range cookies {
		cb.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, cb)
	return rr
}

// The whole OAuth path, driven end to end against a stand-in authorization
// server: start the consent, follow it, land on the callback, and end up with a
// connection an app can spend.
func TestConnectAnOAuthAccountEndToEnd(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cal", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	// Start: hostit answers with where to send the browser
	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"fake-cal","slug":"work-cal","label":"Work calendar"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.NotEmpty(t, started.RedirectURL)

	// The consent URL carries this provider's own parameters
	consent, err := url.Parse(started.RedirectURL)
	require.NoError(t, err)
	assert.Equal(t, "test-client", consent.Query().Get("client_id"))
	assert.Equal(t, "code", consent.Query().Get("response_type"))
	assert.Equal(t, "offline", consent.Query().Get("access_type"))
	assert.True(t, strings.HasPrefix(consent.Query().Get("state"), connectStatePrefix+"fake-cal:work-cal:"),
		"the state carries the provider and the slug through the round trip")

	// hostit set the state cookie the callback compares against
	cookies := append(rr.Result().Cookies(), session)

	// Follow the consent and deliver the callback
	cb := browse(t, s, started.RedirectURL, cookies)
	require.Equal(t, http.StatusFound, cb.Code, cb.Body.String())
	// Back to where connections live. This said /profile until they moved to
	// their own page, so a finished consent dropped people somewhere that no
	// longer showed the thing they had just connected.
	assert.Equal(t, "/connections", cb.Header().Get("Location"))

	// The connection now exists, sealed, under the slug that was asked for
	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "fake-cal", conn.Provider)
	assert.Equal(t, "Work calendar", conn.Label)
	assert.Equal(t, store.ConnectionOAuth, conn.Kind)
	assert.NotContains(t, conn.Secret, "refresh-", "the refresh token is not stored in the clear")
	opened, err := connections.Open(s.connections.key, conn.Secret, connections.Binding(conn.UserID, conn.ID))
	require.NoError(t, err)
	assert.Equal(t, "refresh-1", opened)

	// And an app granted it gets a freshly refreshed access token
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	tok, err := s.connections.tokenFor(context.Background(), a, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "access-1", tok.AccessToken)
	assert.NotNil(t, tok.ExpiresAt)

	// Asked again inside its lifetime, the same token comes back without a
	// second trip to the provider. Past expiry, a fresh one is minted.
	again, err := s.connections.tokenFor(context.Background(), a, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, tok.AccessToken, again.AccessToken)

	s.connections.expireCachedFor(conn.ID)
	fresh, err := s.connections.tokenFor(context.Background(), a, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "access-2", fresh.AccessToken)
}

// Two calendars, connected separately, kept apart end to end.
func TestConnectTwoAccountsOfTheSameProviderEndToEnd(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cal2", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	for _, slug := range []string{"work-cal", "home-cal"} {
		rr := request(t, s.API(), "POST", "/api/connections",
			fmt.Sprintf(`{"provider":"fake-cal2","slug":%q}`, slug), token)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var started apiConnectStartedResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
		cb := browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session))
		require.Equal(t, http.StatusFound, cb.Code, cb.Body.String())
	}

	list, err := s.apps.Store().Connections(u.ID)
	require.NoError(t, err)
	require.Len(t, list, 2, "two separate connections to one provider")

	work, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	home, err := s.apps.Store().ConnectionBySlug(u.ID, "home-cal")
	require.NoError(t, err)
	workSecret, _ := connections.Open(s.connections.key, work.Secret, connections.Binding(work.UserID, work.ID))
	homeSecret, _ := connections.Open(s.connections.key, home.Secret, connections.Binding(home.UserID, home.ID))
	assert.NotEqual(t, workSecret, homeSecret, "each holds its own refresh token")
}

// Re-consenting keeps the connection and its grants, and swaps the credential
// underneath -- the whole reason reconnect exists rather than delete-and-add.
func TestReconnectKeepsTheSlugAndItsGrants(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cal3", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-cal3","slug":"work-cal"}`, token)
	require.Equal(t, http.StatusOK, rr.Code)
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	before, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, request(t, s.API(), "PUT", "/api/apps/dash/connections/work-cal", "", token).Code)

	// Re-consent
	rr = request(t, s.API(), "POST", "/api/connections/work-cal/reconnect", "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	after, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, before.ID, after.ID, "same connection")
	beforeSecret, _ := connections.Open(s.connections.key, before.Secret, connections.Binding(before.UserID, before.ID))
	afterSecret, _ := connections.Open(s.connections.key, after.Secret, connections.Binding(after.UserID, after.ID))
	assert.NotEqual(t, beforeSecret, afterSecret, "fresh credential underneath")

	granted, err := s.apps.Store().AppConnections("a1")
	require.NoError(t, err)
	require.Len(t, granted, 1, "the app never lost access across the re-consent")
}

// A provider whose token does not expire (Slack, GitHub) stores the access
// token itself and hands it straight back.
func TestConnectALongLivedProviderEndToEnd(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.longLived = true
	registerFakeProvider(t, s, f, "fake-slack", true)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-slack","slug":"work-chat"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "work-chat")
	require.NoError(t, err)
	opened, err := connections.Open(s.connections.key, conn.Secret, connections.Binding(conn.UserID, conn.ID))
	require.NoError(t, err)
	assert.Equal(t, "access-0", opened, "the access token is the thing worth keeping")

	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	tok, err := s.connections.tokenFor(context.Background(), a, "work-chat")
	require.NoError(t, err)
	assert.Equal(t, "access-0", tok.AccessToken)
	assert.Equal(t, 0, f.refreshes, "nothing was refreshed, because there is nothing to refresh")
}

// A revoked grant at the provider surfaces as the provider's own words, not a
// generic failure -- that is what tells an owner to reconnect.
func TestARevokedGrantSaysSo(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cal4", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-cal4","slug":"work-cal"}`, token)
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)
	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))

	f.denyRefresh = true
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(context.Background(), a, "work-cal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.Contains(t, err.Error(), "revoked")
}

// The callback is not a way in: a code delivered without a session, or with a
// state that was never issued, creates nothing.
func TestTheCallbackRefusesWithoutASession(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cal5", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-cal5","slug":"work-cal"}`, token)
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))

	// Everything except the session cookie
	cb := browse(t, s, started.RedirectURL, rr.Result().Cookies())
	assert.Equal(t, http.StatusUnauthorized, cb.Code)
	_, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	assert.ErrorIs(t, err, store.ErrConnectionNotFound, "nothing was created")

	// And a state the browser never received is refused by the state check
	cb2 := httptest.NewRequest("GET", "/auth/callback?code=code-1&state="+connectStatePrefix+"fake-cal5:work-cal:forged", nil)
	cb2.AddCookie(signIn(t, s, u))
	rec := httptest.NewRecorder()
	s.API().ServeHTTP(rec, cb2)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "no state cookie to compare against")
}

// The bug this exists to stop: Discord rotates its refresh token on every use,
// so the token hostit stored is dead the moment it is spent. Dropping the new
// one made the first request work and the second fail with invalid_grant --
// which looks exactly like the owner revoking access, and is not.
func TestARotatedRefreshTokenIsStoredSoTheNextRequestWorks(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.rotateRefresh = true
	registerFakeProvider(t, s, f, "fake-rotating", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-rotating","slug":"chat"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "chat")
	require.NoError(t, err)
	require.NoError(t, s.apps.Store().GrantConnection("a1", conn.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)

	// Three trips to the provider. Before the fix the second one failed with
	// invalid_grant. The cache is stepped past deliberately: with it warm, the
	// second request never reaches the provider and would prove nothing.
	for i := 1; i <= 3; i++ {
		tok, err := s.connections.tokenFor(context.Background(), a, "chat")
		require.NoError(t, err, "request %d: the stored refresh token must have been rotated forward", i)
		assert.NotEmpty(t, tok.AccessToken)
		s.connections.expireCachedFor(conn.ID)
	}

	// And what is stored is the newest one, not the one consent handed over.
	after, err := s.apps.Store().Connection(conn.ID)
	require.NoError(t, err)
	stored, err := connections.Open(s.connections.key, after.Secret, connections.Binding(after.UserID, after.ID))
	require.NoError(t, err)
	assert.Equal(t, "refresh-rotated-3", stored)
}

// connect walks a provider's consent for one slug and returns the connection.
func connect(t *testing.T, s *Server, u *store.User, token string, provider, slug string) *store.Connection {
	t.Helper()
	rr := request(t, s.API(), "POST", "/api/connections", fmt.Sprintf(`{"provider":%q,"slug":%q}`, provider, slug), token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), signIn(t, s, u))).Code)
	c, err := s.apps.Store().ConnectionBySlug(u.ID, slug)
	require.NoError(t, err)
	return c
}

// An access token is good for an hour, so minting a fresh one per request is a
// provider round trip -- and, for a rotating provider, a database write -- that
// buys nothing.
func TestAnAccessTokenIsReusedUntilItIsNearlyExpired(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-cached", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	c := connect(t, s, u, token, "fake-cached", "cal")
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)

	first, err := s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	require.Equal(t, 1, f.refreshes)

	for i := 0; i < 5; i++ {
		again, err := s.connections.tokenFor(context.Background(), a, "cal")
		require.NoError(t, err)
		assert.Equal(t, first.AccessToken, again.AccessToken)
	}
	assert.Equal(t, 1, f.refreshes, "five more requests, still one round trip to the provider")

	// Once it is close to expiring, a fresh one is minted rather than served stale
	s.connections.expireCachedFor(c.ID)
	fresh, err := s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	assert.Equal(t, 2, f.refreshes)
	assert.NotEqual(t, first.AccessToken, fresh.AccessToken)
}

// A warm cache must not outlive the grant: revocation is checked against the
// database on every request, before the cache is ever consulted.
func TestRevokingAGrantBeatsAWarmCache(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-revoke", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	c := connect(t, s, u, token, "fake-revoke", "cal")
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)

	_, err = s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)

	require.NoError(t, s.apps.Store().RevokeConnection("a1", c.ID))
	_, err = s.connections.tokenFor(context.Background(), a, "cal")
	assert.ErrorIs(t, err, errNotGranted, "a cached token is not a way around a revoked grant")
}

// Re-consenting replaces the credential underneath, so anything cached from the
// old one has to go -- otherwise the reconnect appears to do nothing for an hour.
func TestReconnectingDropsTheCachedToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-reconn", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	c := connect(t, s, u, token, "fake-reconn", "cal")
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))
	a, err := s.apps.App("dash")
	require.NoError(t, err)

	before, err := s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	require.Equal(t, 1, f.refreshes)

	// Re-consent through the API, exactly as the owner would
	rr := request(t, s.API(), "POST", "/api/connections/cal/reconnect", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), signIn(t, s, u))).Code)

	after, err := s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	assert.Equal(t, 2, f.refreshes, "the new credential was actually used")
	assert.NotEqual(t, before.AccessToken, after.AccessToken)
}

// Removing a connection removes it: the access token minted from it must not
// sit in memory for the rest of its hour. Only OAuth connections are cached --
// a pasted credential costs no round trip -- so this is where it matters.
func TestRemovingAConnectionPurgesItsCachedToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-purge", false)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	c := connect(t, s, u, token, "fake-purge", "cal")
	require.NoError(t, s.apps.Store().GrantConnection("a1", c.ID))

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	_, err = s.connections.tokenFor(context.Background(), a, "cal")
	require.NoError(t, err)
	_, held := s.connections.cached[c.ID]
	require.True(t, held, "it is cached to begin with")

	rr := request(t, s.API(), "DELETE", "/api/connections/cal", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	_, held = s.connections.cached[c.ID]
	assert.False(t, held, "nothing of a removed credential is left in memory")
}

// registerFakeUserProvider registers a user-token provider (Slack personal's
// shape) with three read options, pointed at the fake server.
func registerFakeUserProvider(t *testing.T, s *Server, f *fakeAuthServer, name string) {
	t.Helper()
	connections.Register(connections.Provider{
		Name: name, Label: "Fake " + name, Kind: connections.KindOAuth,
		UserToken: true, LongLivedToken: true,
		Scopes: []string{"users:read"},
		ScopeOptions: []connections.ScopeOption{
			{Key: "public", Label: "Public", Scopes: []string{"channels:read", "channels:history"}, Default: true},
			{Key: "private", Label: "Private", Scopes: []string{"groups:read", "groups:history"}, Default: true},
			{Key: "search", Label: "Search", Scopes: []string{"search:read"}, Default: true},
		},
		AuthURL: f.URL + "/authorize", TokenURL: f.URL + "/token",
		Help: "a test user-token provider",
	})
	if s.config.ConnectionClients == nil {
		s.config.ConnectionClients = map[string]config.OAuthClient{}
	}
	s.config.ConnectionClients[name] = config.OAuthClient{ClientID: "test-client", ClientSecret: "test-secret"}
}

// The chosen read options resolve to user_scope (never the bot scope param), the
// connection records exactly what was granted, and the stored secret is the USER
// token from authed_user -- not the bot token at the top level.
func TestConnectAUserScopeAccountWithChosenScopes(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.longLived = true
	registerFakeUserProvider(t, s, f, "fake-slack-user")
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"fake-slack-user","slug":"myslack","scope_keys":["public","search"]}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))

	consent, err := url.Parse(started.RedirectURL)
	require.NoError(t, err)
	assert.Equal(t, "users:read channels:read channels:history search:read", consent.Query().Get("user_scope"))
	assert.Empty(t, consent.Query().Get("scope"), "a user-token provider must not send the bot scope param")

	cb := browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session))
	require.Equal(t, http.StatusFound, cb.Code, cb.Body.String())

	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "myslack")
	require.NoError(t, err)
	assert.Equal(t, "users:read channels:read channels:history search:read", conn.Scopes, "the connection records exactly what was granted")
	opened, err := connections.Open(s.connections.key, conn.Secret, connections.Binding(conn.UserID, conn.ID))
	require.NoError(t, err)
	assert.Equal(t, "user-access-0", opened, "the user token (authed_user) is stored, not the bot token")
}

// A scope key the provider never offered is refused at connect time, so a
// crafted request cannot smuggle in a scope by naming it.
func TestUnknownScopeKeyIsRefused(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	f.longLived = true
	registerFakeUserProvider(t, s, f, "fake-slack-user2")
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections",
		`{"provider":"fake-slack-user2","slug":"myslack","scope_keys":["public","evil"]}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

// A connection's health follows its refreshes: a provider that rejects the
// refresh token flips it to needs-reconnect (and persists that), and a later
// successful refresh clears it. Drives it through connectionManager.Verify.
func TestConnectionHealthReflectsRefresh(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-health", false) // a refreshing provider
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-health","slug":"cal"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "cal")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, conn.Status, "a fresh connection is healthy")

	// The provider now rejects the refresh: verify reports AND persists needs-reconnect.
	f.denyRefresh = true
	status, err := s.connections.Verify(context.Background(), u.ID, "cal")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, status)
	conn, _ = s.apps.Store().ConnectionBySlug(u.ID, "cal")
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, conn.Status)

	// It recovers: verify clears the flag.
	f.denyRefresh = false
	status, err = s.connections.Verify(context.Background(), u.ID, "cal")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, status)
	conn, _ = s.apps.Store().ConnectionBySlug(u.ID, "cal")
	assert.Equal(t, store.ConnectionStatusOK, conn.Status)
}

// A LONG-LIVED-token connection (Slack/GitHub/Linear class) has nothing to
// refresh, so nothing else ever re-checks it. Verify must actively probe the
// provider: a rejected probe flips it to needs-reconnect (and persists that),
// and a recovering probe clears it. Regression for "Verify never checks a
// long-lived connection".
func TestVerifyProbesLongLivedConnection(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-longlived", true) // long-lived + a ProbeURL
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)

	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-longlived","slug":"ghconn"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	// Healthy: the probe answers 200.
	status, err := s.connections.Verify(context.Background(), u.ID, "ghconn")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, status)

	// The token is revoked at the provider (probe now 401): Verify reports AND
	// persists needs-reconnect -- the whole point, since nothing else would.
	f.denyProbe = true
	status, err = s.connections.Verify(context.Background(), u.ID, "ghconn")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, status)
	conn, _ := s.apps.Store().ConnectionBySlug(u.ID, "ghconn")
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, conn.Status, "persisted")

	// It recovers: the probe answers 200 again and Verify clears the flag.
	f.denyProbe = false
	status, err = s.connections.Verify(context.Background(), u.ID, "ghconn")
	require.NoError(t, err)
	assert.Equal(t, store.ConnectionStatusOK, status)
}

// The proactive sweep must PROBE long-lived-token connections, not skip them:
// they have nothing to refresh, so the sweep is the only thing that would catch
// a token revoked or invalidated at the provider without warning.
func TestPeriodicProbeUpdatesLongLivedHealth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	f := newFakeAuthServer(t)
	registerFakeProvider(t, s, f, "fake-periodic", true)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	session := signIn(t, s, u)
	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"fake-periodic","slug":"conn1"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var started apiConnectStartedResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &started))
	require.Equal(t, http.StatusFound, browse(t, s, started.RedirectURL, append(rr.Result().Cookies(), session)).Code)

	// Revoked at the provider (probe 401): one sweep flips it to needs-reconnect.
	f.denyProbe = true
	s.connections.refreshDue(context.Background())
	conn, _ := s.apps.Store().ConnectionBySlug(u.ID, "conn1")
	assert.Equal(t, store.ConnectionStatusNeedsReconnect, conn.Status, "the sweep probed and flagged it")

	// It recovers on a later sweep.
	f.denyProbe = false
	s.connections.refreshDue(context.Background())
	conn, _ = s.apps.Store().ConnectionBySlug(u.ID, "conn1")
	assert.Equal(t, store.ConnectionStatusOK, conn.Status)
}
