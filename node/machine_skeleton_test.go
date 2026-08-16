package node

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSkeletonFiles(t *testing.T) {
	t.Parallel()
	files := skeletonFiles("blog", "https://blog.apps.example.com", "Go, Python")
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
	// The README must describe what the skeleton ACTUALLY is. It used to claim the
	// old Go-stub era's `mode: app` + `run: hostit placeholder` -- a command that
	// does not exist -- and agents trusting it wrote that run: line into
	// hostit.yml, crash-looping their apps (seen in production).
	assert.NotContains(t, readme, "hostit placeholder")
	assert.NotContains(t, readme, "mode: app`")
	assert.Contains(t, readme, "mode: static")
	// The runtimes blurb already ends with the apt-get hint; the template must not
	// append it a second time ("(install anything else with apt-get) (install...")
	assert.Equal(t, 1, strings.Count(fmt.Sprintf(skeletonAppReadme, "x", "u", "RUNTIMES (install anything else with apt-get)"), "install anything else with apt-get"))
}
