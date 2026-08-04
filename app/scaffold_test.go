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
	assert.Contains(t, files["hostit.yml"], "run: python3 -m http.server $PORT")
	assert.Contains(t, files["README.txt"], "https://blog.apps.example.com")
	assert.Contains(t, files["README.txt"], "10000")
	assert.Contains(t, files["index.html"], "blog is running")
}

func TestDemoPage(t *testing.T) {
	t.Parallel()
	page := demoPage("blog", "apps.example.com")
	assert.Contains(t, page, "<title>blog is running</title>")
	assert.Contains(t, page, "ssh blog@apps.example.com")
	assert.Contains(t, page, "max-width: 560px")
	assert.Contains(t, page, "width: 100%;") // %% unescaped exactly once
	assert.NotContains(t, page, "%%")
	assert.NotContains(t, page, "%!") // No Sprintf argument errors
	assert.False(t, strings.Contains(page, "EXTRA"), "no unused Sprintf arguments")
}
