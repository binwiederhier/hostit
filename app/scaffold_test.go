package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScaffoldFiles(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	files := m.scaffoldFiles("blog", 10000)
	// The app directory holds the app's files and nothing of hostit's own: the
	// platform explains itself through the login banner, "hostit guide" and /docs
	assert.NotContains(t, files, "HOSTIT.txt")
	// A new app runs hostit's built-in placeholder: a real backend, so the app
	// proves it can execute code, not just serve files
	assert.Contains(t, files["hostit.yml"], "mode: app")
	assert.Contains(t, files["hostit.yml"], "hostit placeholder")
	// No static page is scaffolded: the placeholder backend serves its own
	assert.NotContains(t, files, "public/index.html")
	// The README still tells everyone this is a stub and what to build with
	readme := files["README.md"]
	assert.Contains(t, strings.ToLower(readme), "stub")
	assert.Contains(t, readme, "# blog")
	assert.Contains(t, readme, "Go binary")
}
