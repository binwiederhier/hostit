package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatUptime(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "9s", formatUptime(9*time.Second))
	assert.Equal(t, "3m 12s", formatUptime(3*time.Minute+12*time.Second))
	assert.Equal(t, "1h 03m", formatUptime(time.Hour+3*time.Minute+40*time.Second))
}

func TestPlaceholderSnapshotCounts(t *testing.T) {
	t.Parallel()
	s := &placeholderStats{started: time.Now().Add(-90 * time.Second)}
	s.visits.Add(2)
	s.viewers.Add(1)
	snap := s.snapshot()
	assert.Equal(t, int64(2), snap.Visits)
	assert.Equal(t, int64(1), snap.Viewers)
	assert.Equal(t, "1m 30s", snap.Uptime)
	assert.NotEmpty(t, snap.Time)
}

func TestPlaceholderPageServerRendersVisitorNumber(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()

	// Each page load increments the visitor number, rendered into the HTML server
	// side -- proof a real backend handled the request, not a static file.
	first := getBody(t, h, "/")
	require.Equal(t, http.StatusOK, first.Code)
	assert.Contains(t, first.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, first.Body.String(), "placeholder app")
	assert.Contains(t, first.Body.String(), "#1</b>", "first visitor is #1")
	assert.NotContains(t, first.Body.String(), "__VISITOR__", "the token is replaced")

	second := getBody(t, h, "/")
	assert.Contains(t, second.Body.String(), "#2</b>", "second load is visitor #2")
}

func TestPlaceholderLiveStreamPushesStats(t *testing.T) {
	t.Parallel()
	h := placeholderHandler()
	_ = getBody(t, h, "/") // one visit, so visits >= 1

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/live", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, body, "data: ")
	assert.Contains(t, body, `"visits":1`)
	assert.Contains(t, body, `"uptime"`)
	assert.Contains(t, body, `"time"`)
}

func getBody(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}
