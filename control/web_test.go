package control

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hashed Vite assets live under static/media/ and their names change on every
// build, so they must carry the immutable long-cache header; a stale check
// (the old "assets/" prefix) silently drops it and clients refetch forever.
func TestHashedAssetsAreCachedImmutably(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	handler := s.webHandler()

	sub, err := fs.Sub(site, "site")
	require.NoError(t, err)
	matches, err := fs.Glob(sub, assetDir+"*")
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no built assets embedded under %s", assetDir)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+matches[0], nil))
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, assetCacheControl, rr.Header().Get("Cache-Control"))
}

// The SPA shell must never be cached hard, or a deploy leaves clients pinned to
// an old bundle referencing assets that no longer exist.
func TestIndexIsNotCachedImmutably(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.webHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/profile", nil))
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
}
