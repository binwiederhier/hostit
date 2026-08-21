package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// Editing an app's limits is ADMIN-only until per-user pools exist: an owner
// who can raise their own caps has no cap at all.
func TestAppLimitsPatchIsAdminOnly(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()

	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":512}`, token)
	assert.Equal(t, http.StatusForbidden, rr.Code, "the owner may not edit their own caps")

	rr = request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":512}`, testToken)
	assert.Equal(t, http.StatusOK, rr.Code)
}

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

	rr := request(t, s.API(), "PATCH", "/api/apps/blog/limits", `{"memory_mb":768,"cpu_milli":2000}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code)

	rr = request(t, s.API(), "GET", "/api/apps/blog", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.EqualValues(t, 768, resp["memory_limit_mb"], "the effective limit is the override")
	assert.EqualValues(t, 2000, resp["cpu_milli"])
	overrides, ok := resp["limit_overrides"].(map[string]any)
	require.True(t, ok, "the overrides ride along for the UI")
	assert.EqualValues(t, 768, overrides["memory_mb"])
	assert.EqualValues(t, 2000, overrides["cpu_milli"])
	assert.EqualValues(t, 0, overrides["disk_mb"])
}
