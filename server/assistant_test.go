package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

func TestAssistantEndpointDisabledWhenUnconfigured(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // no Anthropic API key -> assistant is nil
	// Authenticated (admin token), but the feature is off: a clear 501, not a panic.
	rr := request(t, s.API(), "POST", "/api/apps/blog/assistant", `{"message":"hi"}`, testToken)
	assert.Equal(t, http.StatusNotImplemented, rr.Code)
}

func TestAssistantEndpointRequiresAuth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps/blog/assistant", `{"message":"hi"}`, "")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// newAssistantTestServer is a test server with the assistant switched on. Its
// model client is never reached by these tests (the requests are refused before a
// turn starts), so a throwaway key is fine.
func newAssistantTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.assistant = assistant.NewManager(assistant.NewClient("test-key"), &appOps{apps: s.apps}, assistant.NewMemoryStore(), "test-model")
	return s
}

func TestAssistantRejectsNonOwner(t *testing.T) {
	t.Parallel()
	s := newAssistantTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "secret", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	other := newActiveTestUser(t, s, "other@example.com")
	otherToken, _, err := s.users.CreateToken(other.ID, "t")
	require.NoError(t, err)

	// A different user cannot read the transcript, open the stream, or start a turn;
	// the app looks like it does not exist, so its existence is not even leaked.
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/secret/assistant", "", otherToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/secret/assistant/stream", "", otherToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/secret/assistant", `{"message":"hi"}`, otherToken).Code)
}

func TestAssistantRefusesCrossSiteRequests(t *testing.T) {
	t.Parallel()
	s := newAssistantTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	value, err := s.sessions.encode(owner.ID)
	require.NoError(t, err)
	cookie := &http.Cookie{Name: s.cookieName(sessionCookieName), Value: value}

	do := func(method, path, body, site string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(cookie)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rr := httptest.NewRecorder()
		s.API().ServeHTTP(rr, req)
		return rr.Code
	}

	// A tenant subdomain page riding the owner's cookie cannot start a turn: a
	// cross-site or sibling-subdomain POST is refused before it reaches the loop.
	assert.Equal(t, http.StatusForbidden, do("POST", "/api/apps/blog/assistant", `{"message":""}`, "cross-site"))
	assert.Equal(t, http.StatusForbidden, do("POST", "/api/apps/blog/assistant", `{"message":""}`, "same-site"))
	// Same-origin passes the CSRF gate (and only then fails on the empty message).
	assert.Equal(t, http.StatusBadRequest, do("POST", "/api/apps/blog/assistant", `{"message":""}`, "same-origin"))
	// The stream refuses a cross-site connection too (defense in depth).
	assert.Equal(t, http.StatusForbidden, do("GET", "/api/apps/blog/assistant/stream", "", "cross-site"))
}
