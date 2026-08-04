package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"heckel.io/hostit/appctl"
)

func TestScaffoldFiles(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	files := m.scaffoldFiles("blog", 10000)
	// The app directory holds the app's files and nothing of hostit's own: the
	// platform explains itself through the login banner, "hostit guide" and /docs
	assert.NotContains(t, files, "HOSTIT.txt")
	// A new app runs as a static stub, so plain HTML works with no runtime
	assert.Contains(t, files["hostit.yml"], "mode: static")
	// Everyone must be told this is a stub, what is installed, and what to build with
	for _, name := range []string{"README.md", appctl.PublicDir + "/index.html"} {
		assert.Contains(t, strings.ToLower(files[name]), "stub", "%s must say it is a stub", name)
		assert.Contains(t, files[name], "python3", "%s must list the runtimes", name)
		assert.Contains(t, files[name], "php", "%s must list the runtimes", name)
		assert.Contains(t, files[name], "Go binary", "%s must suggest the stack", name)
	}
	assert.Contains(t, files["README.md"], "# blog")
}

func TestDemoPage(t *testing.T) {
	t.Parallel()
	page := demoPage("blog", "apps.example.com")
	assert.Contains(t, page, "<title>blog (stub)</title>")
	assert.Contains(t, page, "ssh blog@apps.example.com")
	assert.Contains(t, page, "max-width: 620px")
	assert.Contains(t, page, "width: 100%;") // %% unescaped exactly once
	assert.NotContains(t, page, "%%")
	assert.NotContains(t, page, "%!") // No Sprintf argument errors
}
