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

	_, err := s.AddAppDomain("blog", "blog.example.com")
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
		_, err := s.AddAppDomain("blog", bad)
		assert.ErrorIs(t, err, ErrInvalidDomain, "should reject %q", bad)
	}
}

func TestCustomDomainUniqueAcrossApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))

	_, err := s.AddAppDomain("blog", "shared.example.com")
	require.NoError(t, err)
	_, err = s.AddAppDomain("other", "shared.example.com")
	assert.ErrorIs(t, err, store.ErrAppDomainExists)
}

func TestRemoveCustomDomainStopsRouting(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	_, err := s.AddAppDomain("blog", "blog.example.com")
	require.NoError(t, err)
	waitDomainActive(t, s, "blog.example.com")
	_, ok := s.appNameFromCustomDomain("blog.example.com")
	require.True(t, ok)

	require.NoError(t, s.RemoveAppDomain("blog", "blog.example.com"))
	_, ok = s.appNameFromCustomDomain("blog.example.com")
	assert.False(t, ok, "a removed domain must stop routing")
}

func TestRemoveCustomDomainWrongApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "other", Port: 10001, Host: store.HostLocal}))
	_, err := s.AddAppDomain("blog", "blog.example.com")
	require.NoError(t, err)

	err = s.RemoveAppDomain("other", "blog.example.com")
	assert.ErrorIs(t, err, store.ErrAppDomainNotFound)
}

func TestDomainDNSRecords(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	traffic, delegation := s.DomainDNSRecords("blog", "blog.example.com")
	assert.Equal(t, "blog.example.com", traffic.Name)
	assert.Equal(t, "blog.apps.example.com", traffic.Value)
	assert.Equal(t, "_acme-challenge.blog.example.com", delegation.Name)
	assert.Equal(t, "blog.example.com.acme.apps.example.com", delegation.Value)
}
