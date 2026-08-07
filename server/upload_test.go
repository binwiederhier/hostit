package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeUploadName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"logo.png":              "logo.png",
		"../../etc/logo.png":    "logo.png", // path stripped
		"a b.png":               "a-b.png",  // spaces made safe
		"weird$name!.txt":       "weird-name-.txt",
		"":                      "file",
		"..":                    "file",
		"/":                     "file",
		"sub/dir/photo (1).jpg": "photo--1-.jpg",
	}
	for in, want := range cases {
		assert.Equal(t, want, sanitizeUploadName(in), "sanitize %q", in)
	}
}
