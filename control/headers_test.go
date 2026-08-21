package control

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"heckel.io/hostit/controlconf"
)

// The web app performs privileged admin actions, so its own responses must carry
// the full header set: a CSP, framing denial, nosniff, a Referrer-Policy and,
// under TLS, HSTS across every subdomain.
func TestWebResponsesCarrySecurityHeaders(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := proxyRequest(t, s, "http://apps.example.com/api/health")
	h := rr.Header()
	assert.NotEmpty(t, h.Get("Content-Security-Policy"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.NotEmpty(t, h.Get("Referrer-Policy"))
	assert.Contains(t, h.Get("Strict-Transport-Security"), "max-age=")
}

// A tenant app gets the base headers (nosniff, HSTS) but NOT the CSP or
// X-Frame-Options: those are for our web app, and an app may legitimately want
// to be embedded or to relax content rules for itself.
func TestAppResponsesCarryBaseHeadersOnly(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}))
	defer backend.Close()
	registerAppWithBackend(t, s, "blog", backend.URL)
	rr := proxyRequest(t, s, "http://blog.apps.example.com/")
	h := rr.Header()
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Contains(t, h.Get("Strict-Transport-Security"), "max-age=")
	assert.Empty(t, h.Get("Content-Security-Policy"))
	assert.Empty(t, h.Get("X-Frame-Options"))
}

// HSTS must not be sent when the proxy serves plain HTTP, or a browser would
// refuse to reach a dev instance over http.
func TestHSTSOnlyUnderTLS(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.TLS = controlconf.TLSOff
	rr := proxyRequest(t, s, "http://apps.example.com/api/health")
	assert.Empty(t, rr.Header().Get("Strict-Transport-Security"))
}
