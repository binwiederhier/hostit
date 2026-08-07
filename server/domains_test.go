package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// waitDomainActive waits for a domain's background issuance to settle and the
// routing cache to pick it up. With TLS off in tests there is no cert to obtain,
// so it becomes routable almost immediately.
func waitDomainActive(t *testing.T, s *Server, domain string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, ok := s.appNameFromCustomDomain(domain)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "domain never became routable")
}

func TestCustomDomainRoutesToApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "backend host=%s", r.Host)
	}))
	t.Cleanup(backend.Close)
	registerAppWithBackend(t, s, "blog", backend.URL)

	_, err := s.addAppDomain("blog", "blog.example.com")
	require.NoError(t, err)
	waitDomainActive(t, s, "blog.example.com")

	rr := proxyRequest(t, s, "http://blog.example.com/some/path")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "backend host=blog.example.com") // Host preserved, routed to blog
}

func TestCustomDomainValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))

	for _, bad := range []string{
		"blog.apps.example.com", // under the platform domain -> an app subdomain already
		"apps.example.com",      // the platform's own hostname
		"not a domain",          // garbage
		"example",               // bare label, no dot
	} {
		_, err := s.addAppDomain("blog", bad)
		assert.ErrorIs(t, err, ErrInvalidDomain, "should reject %q", bad)
	}
}

func TestCustomDomainUniqueAcrossApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))

	_, err := s.addAppDomain("blog", "shared.example.com")
	require.NoError(t, err)
	_, err = s.addAppDomain("other", "shared.example.com")
	assert.ErrorIs(t, err, store.ErrAppDomainExists)
}

func TestRemoveCustomDomainStopsRouting(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	_, err := s.addAppDomain("blog", "blog.example.com")
	require.NoError(t, err)
	waitDomainActive(t, s, "blog.example.com")
	_, ok := s.appNameFromCustomDomain("blog.example.com")
	require.True(t, ok)

	require.NoError(t, s.removeAppDomain("blog", "blog.example.com"))
	_, ok = s.appNameFromCustomDomain("blog.example.com")
	assert.False(t, ok, "a removed domain must stop routing")
}

func TestRemoveCustomDomainWrongApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))
	_, err := s.addAppDomain("blog", "blog.example.com")
	require.NoError(t, err)

	err = s.removeAppDomain("other", "blog.example.com")
	assert.ErrorIs(t, err, store.ErrAppDomainNotFound)
}

func TestDomainDNSRecords(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.DNSProvider = "route53" // DNS-01 mode -> a delegation record is included

	records := s.domainDNSRecords("blog", "blog.example.com")
	require.Len(t, records, 2)
	assert.Equal(t, "blog.example.com", records[0].Name)
	assert.Equal(t, "blog.apps.example.com", records[0].Value)
	// Every custom domain delegates to the same fixed challenge name in our zone.
	assert.Equal(t, "_acme-challenge.blog.example.com", records[1].Name)
	assert.Equal(t, "_acme-challenge.acme.apps.example.com", records[1].Value)
}
