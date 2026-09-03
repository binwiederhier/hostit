package control

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// A deleted app is stamped and drops out of its owner's live view but stays for
// admins; the reaper leaves it during the grace, removes it after, and a restore
// brings it back.
func TestSoftDeleteHidesReapsAndRestores(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	require.NoError(t, s.apps.SoftDeleteApp("blog"))
	a, err := st.App("blog")
	require.NoError(t, err)
	assert.False(t, a.SoftDeletedAt.IsZero(), "soft-delete stamps the app")
	owned, err := st.AppsByOwner(u.ID)
	require.NoError(t, err)
	assert.Empty(t, liveApps(owned), "hidden from the owner")
	all, err := st.Apps()
	require.NoError(t, err)
	assert.Len(t, all, 1, "still present for admins")

	// Within the default 7d grace: the reaper leaves it.
	s.ReapSoftDeleted()
	_, err = st.App("blog")
	require.NoError(t, err, "a fresh soft-delete is within the grace")

	// Restore returns it to the owner's live view.
	require.NoError(t, s.apps.RestoreSoftDeleted("blog"))
	owned, _ = st.AppsByOwner(u.ID)
	assert.Len(t, liveApps(owned), 1, "restored to the owner")

	// Past the grace: reaped for real.
	require.NoError(t, st.SetAppSoftDeleted("blog", time.Now().Add(-8*24*time.Hour)))
	s.ReapSoftDeleted()
	_, err = st.App("blog")
	assert.ErrorIs(t, err, store.ErrAppNotFound, "reaped after the grace")
}

// A soft-deleted app is logically gone, so it must stop consuming its owner's
// quota: the memory/disk pool it reserved is released and it no longer counts
// toward the app limit, so the owner can create a replacement at once rather
// than waiting out the grace period.
func TestSoftDeletedAppReleasesQuota(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, MemoryLimitMB: 512, DiskLimitMB: 1024}))

	mem, disk, err := s.poolReserved(u.ID, "")
	require.NoError(t, err)
	require.Greater(t, mem, 0, "a live app reserves its memory from the pool")
	require.Greater(t, disk, 0)
	count, err := st.AppCountByOwner(u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	require.NoError(t, s.apps.SoftDeleteApp("blog"))

	mem, disk, err = s.poolReserved(u.ID, "")
	require.NoError(t, err)
	assert.Equal(t, 0, mem, "a soft-deleted app releases its memory reservation")
	assert.Equal(t, 0, disk, "and its disk reservation")
	count, err = st.AppCountByOwner(u.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "and stops counting toward the app limit")
}

// "Delete now" (purge) hard-deletes a pending-deletion app immediately, skipping
// the rest of its grace; it refuses an app that is not pending deletion.
func TestPurgeSoftDeleted(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "live", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	require.NoError(t, st.AddApp(&store.App{ID: "a2", Name: "shelved", Port: 10001, Host: store.HostLocal, OwnerID: u.ID}))

	// A live app cannot be purged: purge is only for pending-deletion apps.
	rr := request(t, s.API(), "POST", "/api/apps/live/purge", "", testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// Soft-delete, then purge: gone at once, well within the grace.
	require.NoError(t, s.apps.SoftDeleteApp("shelved"))
	rr = request(t, s.API(), "POST", "/api/apps/shelved/purge", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	_, err := st.App("shelved")
	assert.ErrorIs(t, err, store.ErrAppNotFound, "purged immediately, not left for the reaper")
}

// A zero grace deletes at once (on the next reap, kicked off by the delete), but
// the app is still gone from the owner's view immediately.
func TestSoftDeleteZeroGraceReapsImmediately(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.SoftDeleteDuration = "0"
	u := newActiveTestUser(t, s, "owner@example.com")
	st := s.apps.Store()
	require.NoError(t, st.AddApp(&store.App{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	require.NoError(t, s.apps.SoftDeleteApp("blog"))
	s.ReapSoftDeleted()
	_, err := st.App("blog")
	assert.ErrorIs(t, err, store.ErrAppNotFound, "zero grace reaps at once")
}
