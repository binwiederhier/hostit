package control

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPreviewRequest(t *testing.T) {
	t.Parallel()
	// The preview iframe's top-level document carries the query param.
	top := httptest.NewRequest("GET", "http://blog.example.com/?hostit_preview=3", nil)
	assert.True(t, isPreviewRequest(top))
	// Its same-origin sub-resources carry it in the Referer instead.
	asset := httptest.NewRequest("GET", "http://blog.example.com/style.css", nil)
	asset.Header.Set("Referer", "https://blog.example.com/?hostit_preview=3")
	assert.True(t, isPreviewRequest(asset))
	// Ordinary traffic is not a preview.
	plain := httptest.NewRequest("GET", "http://blog.example.com/", nil)
	assert.False(t, isPreviewRequest(plain))
	other := httptest.NewRequest("GET", "http://blog.example.com/?x=1", nil)
	other.Header.Set("Referer", "https://google.com/")
	assert.False(t, isPreviewRequest(other))
}

func TestStripCachingForPreview(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("ETag", `"abc"`)
	h.Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
	h.Set("Expires", "Tue, 02 Jan 2024 00:00:00 GMT")
	stripCachingForPreview(h)
	assert.Equal(t, "no-store, must-revalidate", h.Get("Cache-Control"))
	assert.Empty(t, h.Get("ETag"))
	assert.Empty(t, h.Get("Last-Modified"))
	assert.Empty(t, h.Get("Expires"))
}

// appNameFromHost decides whether a Host reaches an app's container or the front
// door, so its edge cases are security-relevant: only a single label directly
// below the base domain is an app.
func TestAppNameFromHost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // BaseDomain: apps.example.com
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"blog.apps.example.com", "blog", true},
		{"apps.example.com", "", false},          // the base domain itself is not an app
		{"a.b.apps.example.com", "", false},      // a deeper label is not a direct subdomain
		{".apps.example.com", "", false},         // empty label
		{"blog.evil.com", "", false},             // wrong suffix
		{"apps.example.com.evil.com", "", false}, // suffix only in the middle
		{"blog.apps.example.com.", "", false},    // trailing dot is a different suffix
		{"", "", false},
	}
	for _, tc := range cases {
		name, ok := s.appNameFromHost(tc.host)
		assert.Equal(t, tc.ok, ok, "host %q", tc.host)
		assert.Equal(t, tc.want, name, "host %q", tc.host)
	}
}

func TestHostOnly(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "blog.apps.example.com", hostOnly("blog.apps.example.com:443"))
	assert.Equal(t, "blog.apps.example.com", hostOnly("blog.apps.example.com")) // no port
	assert.Equal(t, "127.0.0.1", hostOnly("127.0.0.1:8080"))
}
