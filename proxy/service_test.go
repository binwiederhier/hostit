package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeControl is the control plane's internal surface: a routes endpoint with
// snapshot + long-poll semantics, and app/dashboard upstreams.
type fakeControl struct {
	seq    atomic.Int64
	routes atomic.Value // []Route
	hits   atomic.Int64 // dashboard fallback hits
}

func newFakeControl(routes []Route) *fakeControl {
	f := &fakeControl{}
	f.seq.Store(1)
	f.routes.Store(routes)
	return f
}

func (f *fakeControl) set(routes []Route) {
	f.routes.Store(routes)
	f.seq.Add(1)
}

func (f *fakeControl) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/routes", func(w http.ResponseWriter, r *http.Request) {
		since, _ := fmt.Sscanf("", "") // silence unused
		_ = since
		var sinceSeq int64
		fmt.Sscan(r.URL.Query().Get("since"), &sinceSeq)
		// Long-poll: wait briefly for a newer table
		deadline := time.Now().Add(500 * time.Millisecond)
		for f.seq.Load() == sinceSeq && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = json.NewEncoder(w).Encode(&Table{Seq: f.seq.Load(), Routes: f.routes.Load().([]Route)})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		fmt.Fprintf(w, "control saw host=%s proto=%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	})
	return mux
}

func TestProxyRoutesFromCacheAndFallsBackToControl(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app got host=%s", r.Host)
	}))
	defer appSrv.Close()

	control := newFakeControl([]Route{{Host: "blog.example.com", Target: appSrv.Listener.Addr().String()}})
	controlSrv := httptest.NewServer(control.handler())
	defer controlSrv.Close()

	p := New(&Config{ControlURL: controlSrv.URL, CacheDir: t.TempDir()})
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go p.WatchRoutes(done)
	require.Eventually(t, func() bool { return p.Seq() >= 1 }, 3*time.Second, 10*time.Millisecond)

	// A known app host goes straight to its target, Host preserved
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "blog.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "app got host=blog.example.com", rr.Body.String())

	// An unknown host falls through to control (which owns the 404 page and
	// on-demand certs), with the original Host intact
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "nothing.example.com"
	p.ServeHTTP(rr, req)
	assert.Contains(t, rr.Body.String(), "control saw host=nothing.example.com")
	assert.Contains(t, rr.Body.String(), "proto=https", "the proxy tells control the visitor spoke TLS")

	// A pushed route update takes effect without a restart
	control.set([]Route{{Host: "wiki.example.com", Target: appSrv.Listener.Addr().String()}})
	require.Eventually(t, func() bool { return p.Seq() >= 2 }, 3*time.Second, 10*time.Millisecond)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "wiki.example.com"
	p.ServeHTTP(rr, req)
	assert.Equal(t, "app got host=wiki.example.com", rr.Body.String())
}

func TestProxyServesFromPersistedCacheWhileControlIsDown(t *testing.T) {
	t.Parallel()
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "still serving")
	}))
	defer appSrv.Close()
	cacheDir := t.TempDir()

	// First proxy instance learns the table and persists it.
	control := newFakeControl([]Route{{Host: "blog.example.com", Target: appSrv.Listener.Addr().String()}})
	controlSrv := httptest.NewServer(control.handler())
	p1 := New(&Config{ControlURL: controlSrv.URL, CacheDir: cacheDir})
	done1 := make(chan struct{})
	go p1.WatchRoutes(done1)
	require.Eventually(t, func() bool { return p1.Seq() >= 1 }, 3*time.Second, 10*time.Millisecond)
	close(done1)
	controlSrv.Close() // control goes DOWN

	// A fresh proxy (restart) with control unreachable still routes app traffic.
	p2 := New(&Config{ControlURL: controlSrv.URL, CacheDir: cacheDir})
	done2 := make(chan struct{})
	t.Cleanup(func() { close(done2) })
	go p2.WatchRoutes(done2)
	require.Eventually(t, func() bool { return p2.Seq() >= 1 }, 3*time.Second, 10*time.Millisecond,
		"the persisted table at %s must load without control", filepath.Join(cacheDir, "routes.json"))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://ignored/", nil)
	req.Host = "blog.example.com"
	p2.ServeHTTP(rr, req)
	assert.Equal(t, "still serving", rr.Body.String())
}
