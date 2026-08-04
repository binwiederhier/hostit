package server

import (
	"net/http"
	"net/http/httptest"
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
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
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
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
	// ... but /v1/account works, so the web app can show "waiting for approval"
	req = httptest.NewRequest("GET", "/v1/account", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
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
	for _, path := range []string{"/v1/users", "/v1/settings"} {
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
	assert.Equal(t, stateCookieName, cookies[0].Name)
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
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "right"})
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
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "st"})
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	assert.Equal(t, "/", rr.Header().Get("Location"))
	var session *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
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
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "st"})
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
	assert.Equal(t, sessionCookieName, cookies[0].Name)
	assert.Empty(t, cookies[0].Value)
	assert.True(t, cookies[0].MaxAge < 0)
}
