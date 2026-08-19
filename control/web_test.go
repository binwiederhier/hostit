package control

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireBuiltWebApp skips a test that needs the React app embedded. Only
// .gitkeep is committed under control/site, so `go test ./...` in a checkout
// that has not run `make web` has no UI to assert on -- and a cache-header test
// that quietly passes against a missing file would be worse than a skip. The
// release path builds the web app before it runs the tests, so these do run
// where it matters.
func requireBuiltWebApp(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(site, "site")
	require.NoError(t, err)
	if _, err := fs.Stat(sub, indexFile); err != nil {
		t.Skip("web app not built (run `make web`)")
	}
	return sub
}

// Hashed Vite assets live under static/media/ and their names change on every
// build, so they must carry the immutable long-cache header; a stale check
// (the old "assets/" prefix) silently drops it and clients refetch forever.
func TestHashedAssetsAreCachedImmutably(t *testing.T) {
	t.Parallel()
	requireBuiltWebApp(t)
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
	requireBuiltWebApp(t)
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.webHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/profile", nil))
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
}
