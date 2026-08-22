package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// The PATCH is partial: absent/0 leaves a field alone, -1 clears the override,
// positive sets it -- and what lands is visible in the store, in control's
// effective record, and in the response.
func TestAppLimitsPatchSetsAndClears(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	s.apps.PushMirror()

	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":512,"cpu_milli":1500}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, err := s.apps.Store().App("blog")
	require.NoError(t, err)
	assert.Equal(t, 512, a.MemoryLimitMB)
	assert.Equal(t, 1500, a.CPUMilli)
	assert.Zero(t, a.DiskLimitMB, "an absent field stays untouched")
	assert.Equal(t, 512, s.apps.MemoryLimit("blog"), "the effective record follows immediately")
	assert.Equal(t, 1500, s.apps.CPULimit("blog"))

	// Partial: setting disk leaves the memory override alone.
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"disk_mb":4096}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	a, err = s.apps.Store().App("blog")
	require.NoError(t, err)
	assert.Equal(t, 512, a.MemoryLimitMB)
	assert.Equal(t, 4096, a.DiskLimitMB)

	// -1 clears an override (back to the owner default / uncapped CPU).
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"cpu_milli":-1}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	a, err = s.apps.Store().App("blog")
	require.NoError(t, err)
	assert.Zero(t, a.CPUMilli)

	// Floors: a cap too small to run anything is refused, not applied.
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":16}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"cpu_milli":50}`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// The app response shows what is ENFORCED (override where set, else the
// owner's default), plus the overrides themselves so the UI can tell an
// inherited value from an admin-set one.
func TestAppResponseCarriesEffectiveLimits(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()

	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":256,"cpu_milli":2000}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = request(t, s.API(), "GET", "/api/apps/blog", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.EqualValues(t, 256, resp["memory_limit_mb"], "the effective limit is the override")
	assert.EqualValues(t, 2000, resp["cpu_milli"])
	overrides, ok := resp["limit_overrides"].(map[string]any)
	require.True(t, ok, "the overrides ride along for the UI")
	assert.EqualValues(t, 256, overrides["memory_mb"])
	assert.EqualValues(t, 2000, overrides["cpu_milli"])
	assert.EqualValues(t, 0, overrides["disk_mb"])
}

// Owners edit their own apps' RAM and disk WITHIN their pool; the pool is the
// cap, not an admin. CPU stays admin-only, and an app-scoped (agent) token
// may not edit limits at all -- an assistant must not raise its own caps.
func TestOwnerEditsLimitsWithinPool(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()

	// Within the default pool: fine.
	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":256}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, err := s.apps.Store().App("blog")
	require.NoError(t, err)
	assert.Equal(t, 256, a.MemoryLimitMB)

	// Beyond the pool: refused, and the error names the budget.
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":2048}`, token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "pool")

	// CPU is not the owner's to set.
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"cpu_milli":2000}`, token)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	// The app's own agent token is refused outright.
	agent := appScopedToken(t, s, u, "blog")
	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":512}`, agent)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// The pool binds admins too: one invariant, no bypass -- an admin who needs
// more raises the user's pool. The sum counts every app of the owner.
func TestPoolBindsAcrossAppsAndAdmins(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	// An explicit pool, so this pins the ARITHMETIC rather than whatever the
	// shipped default happens to be.
	pool384 := 384
	u.MemoryPoolMB = &pool384
	require.NoError(t, s.apps.Store().UpdateUser(u))

	// Two apps at the 128 default; the second override must fit POOL - sum(others).
	// Pool 384: blog to 256 fits (256 + 128), then wiki to 256 would need
	// 256 + 256 > 384 and is refused -- for the ADMIN too.
	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":256}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = request(t, s.API(), "PATCH", "/api/apps/wiki/limits", `{"memory_mb":256}`, testToken)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "pool")

	// Raising the user's pool unblocks it.
	pool := 4096
	u.MemoryPoolMB = &pool
	require.NoError(t, s.apps.Store().UpdateUser(u))
	rr = request(t, s.API(), "PATCH", "/api/apps/wiki/limits", `{"memory_mb":256}`, testToken)
	assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// Creating an app reserves its default allocation from the pool, so a user
// whose pool is spent cannot mint another app into it.
func TestCreateRefusedWhenPoolExhausted(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	pool := 200 // one 128 MB app fits, a second does not
	u.MemoryPoolMB = &pool
	require.NoError(t, s.apps.Store().UpdateUser(u))

	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	rr = request(t, s.API(), "POST", "/api/apps", `{"name":"wiki"}`, token)
	require.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "pool")
}
