package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaceholderServesStaticPage(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()

	rr := getBody(t, h, "/")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
	// The redesigned placeholder is a simple static page.
	assert.Contains(t, rr.Body.String(), "Nothing here yet")
	assert.Contains(t, rr.Body.String(), "hostit")
}

func TestPlaceholderIsStaticNoLiveEndpoint(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()

	// The old placeholder streamed live stats at /live; the static one does not.
	rr := getBody(t, h, "/live")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPlaceholderUnknownPath404(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()

	rr := getBody(t, h, "/nope")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func getBody(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}
