package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// The internal surface is what hostit-proxy (and later hostit-node) consume:
// a sequenced routing table with long-poll semantics.
func TestInternalRoutesSnapshotAndLongPoll(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	// Snapshot: the app's public host maps to its loopback target
	rr := httptest.NewRecorder()
	s.Internal().ServeHTTP(rr, httptest.NewRequest("GET", "/internal/routes", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var table routeTable
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &table))
	require.NotZero(t, table.Seq)
	require.Len(t, table.Routes, 1)
	assert.Equal(t, "blog.apps.example.com", table.Routes[0].Host)
	assert.Equal(t, "127.0.0.1:10000", table.Routes[0].Target)

	// Long-poll: same seq blocks until a change lands (an app is added), then
	// returns the new table immediately.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID})
	}()
	start := time.Now()
	rr = httptest.NewRecorder()
	s.Internal().ServeHTTP(rr, httptest.NewRequest("GET", "/internal/routes?since="+strconv.FormatInt(table.Seq, 10)+"&timeout=3", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var next routeTable
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &next))
	assert.Greater(t, next.Seq, table.Seq, "a change bumps the seq")
	assert.Len(t, next.Routes, 2)
	assert.Less(t, time.Since(start), 3*time.Second, "the poll returns on change, not only at timeout")
}

func TestInternalRoutesIncludeActiveCustomDomains(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddDomain(&store.Domain{Domain: "www.phil.example", AppName: "blog", Status: store.DomainActive}))

	rr := httptest.NewRecorder()
	s.Internal().ServeHTTP(rr, httptest.NewRequest("GET", "/internal/routes", nil))
	var table routeTable
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &table))
	hosts := map[string]string{}
	for _, r := range table.Routes {
		hosts[r.Host] = r.Target
	}
	assert.Equal(t, "127.0.0.1:10000", hosts["blog.apps.example.com"])
	assert.Equal(t, "127.0.0.1:10000", hosts["www.phil.example"], "an active custom domain routes to the same app")
}
