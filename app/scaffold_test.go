package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScaffoldFiles(t *testing.T) {
	t.Parallel()
	files := scaffoldFiles("blog", "https://blog.apps.example.com", "Go, Python")
	// The app directory holds the app's files and nothing of hostit's own: the
	// platform explains itself through the login banner, "hostit guide" and /docs
	assert.NotContains(t, files, "HOSTIT.txt")
	// A new app is a static app serving public/, which starts with a placeholder
	// index.html the owner replaces; no hostit process runs for a fresh app
	assert.Contains(t, files["hostit.yml"], "mode: static")
	assert.NotContains(t, files["hostit.yml"], "hostit placeholder")
	assert.Contains(t, files, "public/index.html")
	assert.Contains(t, files["public/index.html"], "Nothing here yet")
	// The README still tells everyone this is a stub and what to build with
	readme := files["README.md"]
	assert.Contains(t, strings.ToLower(readme), "stub")
	assert.Contains(t, readme, "# blog")
	assert.Contains(t, readme, "Go binary")
}
