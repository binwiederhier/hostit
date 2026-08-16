package control

import (
	"encoding/json"
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

// The custom-domain HTTP surface: add records a pending domain (201) and returns
// the DNS records the owner must create; cert issuance is a background goroutine,
// so we assert the response and the stored row, not the eventual active state.
func TestAppDomainAddListVerifyDelete(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	// Empty to begin with
	rr := request(t, s.API(), "GET", "/api/apps/blog/domains", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "[]\n", rr.Body.String())

	// Add attaches the domain and returns the DNS records to create
	rr = request(t, s.API(), "POST", "/api/apps/blog/domains", `{"domain":"blog.example.com"}`, token)
	require.Equal(t, http.StatusCreated, rr.Code)
	var added apiAppDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &added))
	assert.Equal(t, "blog.example.com", added.Domain)
	assert.NotEmpty(t, added.DNS, "the owner needs at least the traffic record")

	// The store records it against the app right away, before any cert exists
	d, err := s.apps.Store().Domain("blog.example.com")
	require.NoError(t, err)
	assert.Equal(t, "blog", d.AppName)

	// It now shows up in the list
	rr = request(t, s.API(), "GET", "/api/apps/blog/domains", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var list []*apiAppDomainResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "blog.example.com", list[0].Domain)

	// Verify re-attempts issuance in the background and answers 200
	rr = request(t, s.API(), "POST", "/api/apps/blog/domains/blog.example.com/verify", "", token)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Delete detaches it, and it is gone from the store
	rr = request(t, s.API(), "DELETE", "/api/apps/blog/domains/blog.example.com", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	_, err = s.apps.Store().Domain("blog.example.com")
	assert.ErrorIs(t, err, store.ErrAppDomainNotFound)
}

func TestAppDomainAddRejectsBadDomain(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	rr := request(t, s.API(), "POST", "/api/apps/blog/domains", `{"domain":"not a domain"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// The custom-domain endpoints are app-scoped just like the rest: a non-owner sees
// someone else's app as a 404, so the domain surface never leaks it either.
func TestAppDomainEndpointsAreOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "secret", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	strangerToken := accountToken(t, s, stranger)

	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/secret/domains", "", strangerToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/secret/domains", `{"domain":"x.example.com"}`, strangerToken).Code)
}

// Verify and delete only accept a domain that belongs to the named app, so one
// app cannot manage another's domain even by guessing the name.
func TestAppDomainVerifyDeleteWrongApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	_, err := s.addAppDomain("blog", "blog.example.com")
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/wiki/domains/blog.example.com/verify", "", token).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "DELETE", "/api/apps/wiki/domains/blog.example.com", "", token).Code)
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
