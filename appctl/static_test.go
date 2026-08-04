package appctl

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticHandlerServesFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>home</h1>"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "css"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "css", "site.css"), []byte("body{}"), 0644))
	h := StaticHandler(dir)

	// The index is served for the root
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "home")

	// ... and normal files by path
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/css/site.css", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/css")
}

func TestStaticHandlerMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>home</h1>"), 0644))
	h := StaticHandler(dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/nope.txt", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStaticHandlerNoDirectoryListing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "private"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "private", "secret.txt"), []byte("shh"), 0644))
	h := StaticHandler(dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/private/", nil))
	// A folder without an index must not enumerate its contents
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.NotContains(t, rr.Body.String(), "secret.txt")
}

func TestStaticHandlerDoesNotEscapeRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("nope"), 0644))
	h := StaticHandler(dir)
	for _, path := range []string{"/../outside.txt", "/%2e%2e/outside.txt"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		assert.NotContains(t, rr.Body.String(), "nope", "path %s must not escape the root", path)
	}
}

func TestStaticCommandInConfig(t *testing.T) {
	t.Parallel()
	c := &AppConfig{Static: "public"}
	require.NoError(t, c.Validate())
	assert.Equal(t, `/usr/bin/hostit static --dir "public"`, c.Command("/usr/bin/hostit"))
	// Other modes keep their own command
	c = &AppConfig{Run: "./server"}
	require.NoError(t, c.Validate())
	assert.Equal(t, "./server", c.Command("/usr/bin/hostit"))
}
