package control

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/node/api"
)

// relayRequest sends one request through a node's relay handler, the way the
// node's app socket does: prefixed path, app named in the header.
func relayRequest(handler http.Handler, method, path, app string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, api.AppRelayPrefix+path, nil)
	if app != "" {
		req.Header.Set(api.AppRelayHeader, app)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// The relay serves an app's /v1 surface with control's guards intact: the same
// archived app that refuses to deploy over the REST API refuses through the
// relay, because it is the same handler behind a different resolver. This is
// the "relay, don't answer locally" contract from the node's side of the fix.
func TestAppRelayKeepsControlsGuards(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, err := s.apps.CreateApp("blog", nil)
	require.NoError(t, err)
	s.apps.WaitBackground()
	handler := s.AppRelayHandler("local") // the app's host

	rr := relayRequest(handler, "GET", "/v1/self", "blog")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"blog"`)

	require.NoError(t, s.apps.Archive("blog"))
	rr = relayRequest(handler, "POST", "/v1/self/deploy", "blog")
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "archived", "the guard fires through the relay")
}

// A node may only speak for apps it hosts: the same scoping the snapshot and
// usage callbacks enforce. Without it, one compromised node could drive any
// app in the cluster by naming it.
func TestAppRelayRefusesAppsTheNodeDoesNotHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, err := s.apps.CreateApp("blog", nil)
	require.NoError(t, err)
	s.apps.WaitBackground()

	rr := relayRequest(s.AppRelayHandler("some-other-node"), "GET", "/v1/self", "blog")
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "not hosted by this node")

	rr = relayRequest(s.AppRelayHandler("local"), "GET", "/v1/self", "")
	assert.Equal(t, http.StatusBadRequest, rr.Code, "a relay request must name an app")
}

// The relay header means something ONLY on a node's authenticated channel. On
// the public API it is inert: authentication still comes from the session or
// token, and a request carrying the header with no credentials is refused
// exactly like one without it.
func TestRelayHeaderIsInertOffTheClusterLink(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, err := s.apps.CreateApp("blog", nil)
	require.NoError(t, err)
	s.apps.WaitBackground()

	req := httptest.NewRequest("POST", "/api/apps/blog/deploy", nil)
	req.Header.Set(api.AppRelayHeader, "blog")
	rr := httptest.NewRecorder()
	s.API().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code, "the header grants nothing on the REST API")

	// And control's own socket still resolves by peer uid, never by header: a
	// header naming an app while the peer uid is unknown resolves nothing.
	req = httptest.NewRequest("GET", "/v1/self", nil)
	req.Header.Set(api.AppRelayHeader, "blog")
	rr = httptest.NewRecorder()
	s.socketHandler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code, "no peer creds means no app, whatever the header says")
}
