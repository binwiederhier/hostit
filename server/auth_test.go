package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestSessionCookieRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	value, err := s.sessions.encode("u_abc")
	require.NoError(t, err)
	userID, err := s.sessions.decode(value)
	require.NoError(t, err)
	assert.Equal(t, "u_abc", userID)
}

func TestSessionCookieTampered(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	value, err := s.sessions.encode("u_abc")
	require.NoError(t, err)
	_, err = s.sessions.decode(value + "x")
	require.Error(t, err)
	_, err = s.sessions.decode("garbage")
	require.Error(t, err)
	// A cookie signed with a different key must not validate
	other := newSessionManager("different-key")
	otherValue, err := other.encode("u_abc")
	require.NoError(t, err)
	_, err = s.sessions.decode(otherValue)
	require.Error(t, err)
}

func TestSessionCookieExpired(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.sessions.ttl = -time.Minute // Everything is already expired
	value, err := s.sessions.encode("u_abc")
	require.NoError(t, err)
	_, err = s.sessions.decode(value)
	require.Error(t, err)
}

func TestAuthWithSessionCookie(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "phil@example.com")
	req := httptest.NewRequest("GET", "/v1/account", nil)
	value, err := s.sessions.encode(u.ID)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: value})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "phil@example.com")
}

func TestAuthWithUserToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "phil@example.com")
	token, _, err := s.users.CreateToken(u.ID, "claude")
	require.NoError(t, err)
	rr := request(t, s.API(), "GET", "/v1/account", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "phil@example.com")
}

func TestAuthWithGlobalAdminToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/v1/account", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"role":"admin"`)
}

func TestAuthRejectsUnknownCredentials(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "GET", "/v1/account", "", "hostit_bogus")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	rr = request(t, s.API(), "GET", "/v1/account", "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthRejectsPendingUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u, err := s.users.Login("pending@example.com", "Pending")
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/v1/apps", nil)
	value, err := s.sessions.encode(u.ID)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: value})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
	// ... but /v1/account works, so the web app can show "waiting for approval"
	req = httptest.NewRequest("GET", "/v1/account", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: value})
	rr = httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"status":"pending"`)
}

func TestAdminOnlyEndpoints(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "user@example.com")
	token, _, err := s.users.CreateToken(u.ID, "t")
	require.NoError(t, err)
	for _, path := range []string{"/v1/users", "/v1/settings", "/v1/domains"} {
		rr := request(t, s.API(), "GET", path, "", token)
		require.Equal(t, http.StatusForbidden, rr.Code, "path %s", path)
		rr = request(t, s.API(), "GET", path, "", testToken)
		require.Equal(t, http.StatusOK, rr.Code, "path %s", path)
	}
}

func TestWebIsServedOnTheBaseDomain(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// The base domain is the front door; the old hostit.<base> keeps working so
	// links and prompts handed out earlier do not break
	for _, host := range []string{"apps.example.com", "hostit.apps.example.com"} {
		rr := proxyRequest(t, s, "http://"+host+"/v1/health")
		assert.Equal(t, http.StatusOK, rr.Code, "host %s must serve the API", host)
	}
	// An app subdomain still goes to the app, not the web UI
	rr := proxyRequest(t, s, "http://nosuchapp.apps.example.com/v1/health")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestOAuthRedirectFollowsTheHostInUse(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	assert.Equal(t, "https://apps.example.com/auth/callback", s.config.RedirectURL("apps.example.com"))
	assert.Equal(t, "https://hostit.apps.example.com/auth/callback", s.config.RedirectURL("hostit.apps.example.com"))
	// Anything else falls back to the canonical hostname
	assert.Equal(t, "https://apps.example.com/auth/callback", s.config.RedirectURL("evil.example.org"))
}

func TestGoogleLoginRedirect(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	req := httptest.NewRequest("GET", "/auth/google", nil)
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	location := rr.Header().Get("Location")
	assert.Contains(t, location, "accounts.google.com")
	assert.Contains(t, location, "client_id=client-id")
	assert.Contains(t, location, "state=")
	// The state is also set as a cookie, so the callback can verify it
	cookies := rr.Result().Cookies()
	require.NotEmpty(t, cookies)
	assert.Equal(t, s.cookieName(stateCookieName), cookies[0].Name)
}

func TestGoogleLoginDisabledWithoutConfig(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // No Google credentials configured
	req := httptest.NewRequest("GET", "/auth/google", nil)
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotImplemented, rr.Code)
}

func TestGoogleCallbackStateMismatch(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	req := httptest.NewRequest("GET", "/auth/callback?code=abc&state=wrong", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: "right"})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestGoogleCallbackCreatesSession(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	// Stub the token exchange; the real one talks to Google
	s.exchangeGoogleCode = func(code, host string) (*googleIdentity, error) {
		assert.Equal(t, "the-code", code)
		return &googleIdentity{Email: "new@example.com", Name: "New Person", EmailVerified: true}, nil
	}
	req := httptest.NewRequest("GET", "/auth/callback?code=the-code&state=st", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: "st"})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "/", rr.Header().Get("Location"))
	var session *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == s.cookieName(sessionCookieName) {
			session = c
		}
	}
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly)
	// The user now exists, pending approval
	u, err := s.users.Login("new@example.com", "")
	require.NoError(t, err)
	assert.Equal(t, store.StatusPending, u.Status)
}

func TestGoogleCallbackRejectsUnverifiedEmail(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.GoogleClientID = "client-id"
	s.config.GoogleClientSecret = "secret"
	s.exchangeGoogleCode = func(code, host string) (*googleIdentity, error) {
		return &googleIdentity{Email: "spoof@example.com", EmailVerified: false}, nil
	}
	req := httptest.NewRequest("GET", "/auth/callback?code=c&state=st", nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName(stateCookieName), Value: "st"})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestLogoutClearsCookie(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.NotEmpty(t, cookies)
	assert.Equal(t, s.cookieName(sessionCookieName), cookies[0].Name)
	assert.Empty(t, cookies[0].Value)
	assert.True(t, cookies[0].MaxAge < 0)
}

func TestAppTokenCannotReachTheAccountSurface(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	token, _, err := s.users.CreateAppToken(u.ID, "blog", "agent")
	require.NoError(t, err)

	// This token gets pasted into a third-party AI assistant. It must not be
	// able to tell that assistant who the owner is or what else they have.
	rr := request(t, s.API(), "GET", "/v1/account", "", token)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.NotContains(t, rr.Body.String(), "owner@example.com")
	// Its own app still works
	rr = request(t, s.API(), "GET", "/api/blog/info", "", token)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestOwnerlessAppTokenDoesNotPanic(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// An app created with the global admin token has no owner, but its agent
	// token still works; nothing may dereference the user behind it
	rr := request(t, s.API(), "POST", "/v1/apps", `{"name":"orphan"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	var created apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.AgentToken)
	rr = request(t, s.API(), "GET", "/v1/account", "", created.AgentToken)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestSessionCookieCannotBeShadowedByAnApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// Apps live on subdomains of the same registrable domain, so a tenant's page
	// can set a cookie for the parent domain. The __Host- prefix makes the
	// browser refuse any cookie with a Domain attribute, which is the only thing
	// that stops an app from planting a session on the web app.
	name := s.cookieName(sessionCookieName)
	assert.True(t, strings.HasPrefix(name, "__Host-"),
		"the session cookie must carry the __Host- prefix, got %q", name)
	c := s.cookie(name, "value", 60)
	assert.Empty(t, c.Domain, "__Host- forbids a Domain attribute")
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
}

func TestCookieAuthRejectsCrossSiteWrites(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	session, err := s.sessions.encode(u.ID)
	require.NoError(t, err)

	withCookie := func(method, path, body, fetchSite, origin string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookieName), Value: session})
		req.Header.Set("Content-Type", "application/json")
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rr := httptest.NewRecorder()
		s.API().ServeHTTP(rr, req)
		return rr.Code
	}

	// A form or fetch from an app's page must not act as the signed-in user:
	// this is how a tenant would make itself an admin
	assert.Equal(t, http.StatusForbidden, withCookie("POST", "/v1/apps", `{"name":"evil"}`, "cross-site", "https://blog.apps.example.com"))
	assert.Equal(t, http.StatusForbidden, withCookie("POST", "/v1/apps", `{"name":"evil"}`, "", "https://blog.apps.example.com"))
	// The web app itself keeps working, with either signal
	assert.Equal(t, http.StatusCreated, withCookie("POST", "/v1/apps", `{"name":"mine"}`, "same-origin", ""))
	assert.Equal(t, http.StatusCreated, withCookie("POST", "/v1/apps", `{"name":"mine2"}`, "", "https://apps.example.com"))
	// Reads are not state-changing, so they are left alone
	assert.Equal(t, http.StatusOK, withCookie("GET", "/v1/apps", "", "cross-site", "https://blog.apps.example.com"))
}
