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

	"heckel.io/hostit/connections"
	"heckel.io/hostit/controlconf"
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
			body := map[string]any{"access_token": "access-0", "expires_in": 3600}
			if !f.longLived {
				body["refresh_token"] = refresh
			}
			_ = json.NewEncoder(w).Encode(body)
		case "refresh_token":
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

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
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
		Help:           "a test provider",
	})
	if s.config.ConnectionClients == nil {
		s.config.ConnectionClients = map[string]controlconf.OAuthClient{}
	}
	s.config.ConnectionClients[name] = controlconf.OAuthClient{ClientID: "test-client", ClientSecret: "test-secret"}
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
	assert.Equal(t, "/profile", cb.Header().Get("Location"))

	// The connection now exists, sealed, under the slug that was asked for
	conn, err := s.apps.Store().ConnectionBySlug(u.ID, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "fake-cal", conn.Provider)
	assert.Equal(t, "Work calendar", conn.Label)
	assert.Equal(t, store.ConnectionOAuth, conn.Kind)
	assert.NotContains(t, conn.Secret, "refresh-", "the refresh token is not stored in the clear")
	opened, err := connections.Open(s.connections.key, conn.Secret)
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

	// Asked again, it refreshes again rather than serving a stale token
	tok, err = s.connections.tokenFor(context.Background(), a, "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "access-2", tok.AccessToken)
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
	workSecret, _ := connections.Open(s.connections.key, work.Secret)
	homeSecret, _ := connections.Open(s.connections.key, home.Secret)
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
	beforeSecret, _ := connections.Open(s.connections.key, before.Secret)
	afterSecret, _ := connections.Open(s.connections.key, after.Secret)
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
	opened, err := connections.Open(s.connections.key, conn.Secret)
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
