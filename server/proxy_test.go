package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
