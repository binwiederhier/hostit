package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/proxyapi"
)

// table is what control would push at this proxy.
func table(seq int64, routes ...proxyapi.Route) *proxyapi.Table {
	return &proxyapi.Table{Seq: seq, Routes: routes}
}

// The proxy serves whatever control last told it: a known host goes straight
// to the app, everything else falls through to control, which owns the
// "nothing here" page.
func TestProxyRoutesWhatControlPushedAndFallsBackToControl(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app got host=%s", r.Host)
	}))
	defer appSrv.Close()
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "control saw host=%s proto=%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	}))
	defer controlSrv.Close()

	p := New(&Config{ControlURL: controlSrv.URL, CacheDir: t.TempDir()})
	require.NoError(t, p.ApplyRoutes(table(1, proxyapi.Route{Host: "blog.example.com", Target: appSrv.Listener.Addr().String()})))

	// A known app host goes straight to its target, Host preserved
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "blog.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "app got host=blog.example.com", rr.Body.String())

	// An unknown host falls through to control, with the original Host intact
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "nothing.example.com"
	p.ServeHTTP(rr, req)
	assert.Contains(t, rr.Body.String(), "control saw host=nothing.example.com")
	assert.Contains(t, rr.Body.String(), "proto=https", "the proxy tells control the visitor spoke TLS")

	// A newer table takes effect without a restart
	require.NoError(t, p.ApplyRoutes(table(2, proxyapi.Route{Host: "wiki.example.com", Target: appSrv.Listener.Addr().String()})))
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "wiki.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "app got host=wiki.example.com", rr.Body.String())
}

// Pushes can overlap (a connect and a change can race), so an older table must
// be discarded rather than applied -- applying one would un-route apps that
// already exist.
func TestAnOlderTableIsIgnored(t *testing.T) {
	t.Parallel()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})
	require.NoError(t, p.ApplyRoutes(table(7, proxyapi.Route{Host: "blog.example.com", Target: "10.0.0.1:10000"})))
	require.NoError(t, p.ApplyRoutes(table(3, proxyapi.Route{Host: "gone.example.com", Target: "10.0.0.1:10001"})))
	assert.Equal(t, int64(7), p.Seq())
	assert.Equal(t, 1, p.Heartbeat().Routes, "the newer table is still the one being served")
	require.Len(t, p.table.Load().(*proxyapi.Table).Routes, 1)
	assert.Equal(t, "blog.example.com", p.table.Load().(*proxyapi.Table).Routes[0].Host)
}

// A proxy that restarts while control is unreachable still serves: the last
// table control pushed is on disk, which is the whole reason it is cached.
func TestProxyServesFromPersistedCacheWhileControlIsDown(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "still serving")
	}))
	defer appSrv.Close()
	cacheDir := t.TempDir()

	// The first instance is told the table and persists it.
	p1 := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: cacheDir})
	require.NoError(t, p1.ApplyRoutes(table(1, proxyapi.Route{Host: "blog.example.com", Target: appSrv.Listener.Addr().String()})))

	// A fresh instance (a restart), with control unreachable, routes anyway.
	p2 := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: cacheDir})
	require.Equal(t, int64(1), p2.Seq(),
		"the persisted table at %s must load without control", filepath.Join(cacheDir, "routes.json"))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "blog.example.com"
	p2.ServeHTTP(rr, req)
	assert.Equal(t, "still serving", rr.Body.String())
}
