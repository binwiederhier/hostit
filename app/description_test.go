package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetYAMLDescription(t *testing.T) {
	t.Parallel()

	// Replaces an existing description: line, leaving the rest intact.
	got := setYAMLDescription("mode: static\ndescription: old thing\nrun: x\n", "new one")
	assert.Contains(t, got, `description: "new one"`)
	assert.NotContains(t, got, "old thing")
	assert.Contains(t, got, "mode: static")
	assert.Contains(t, got, "run: x")

	// Inserts one when absent.
	got = setYAMLDescription("mode: static\n", "hello")
	assert.Contains(t, got, `description: "hello"`)
	assert.Contains(t, got, "mode: static")

	// Empty document.
	assert.Equal(t, "description: \"hi\"\n", setYAMLDescription("", "hi"))

	// Quotes special characters.
	got = setYAMLDescription("", `a "b" \c`)
	assert.Contains(t, got, `description: "a \"b\" \\c"`)

	// A commented example is not treated as the real key; a new line is added.
	got = setYAMLDescription("# description: example\nmode: static\n", "real")
	assert.Contains(t, got, `description: "real"`)
	assert.Contains(t, got, "# description: example")
}
