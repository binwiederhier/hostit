package appconf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetDescription(t *testing.T) {
	t.Parallel()

	// Replaces an existing description: line, leaving the rest intact.
	got := SetDescription("mode: static\ndescription: old thing\nrun: x\n", "new one")
	assert.Contains(t, got, `description: "new one"`)
	assert.NotContains(t, got, "old thing")
	assert.Contains(t, got, "mode: static")
	assert.Contains(t, got, "run: x")

	// Inserts one when absent.
	got = SetDescription("mode: static\n", "hello")
	assert.Contains(t, got, `description: "hello"`)
	assert.Contains(t, got, "mode: static")

	// Empty document.
	assert.Equal(t, "description: \"hi\"\n", SetDescription("", "hi"))

	// Quotes special characters.
	got = SetDescription("", `a "b" \c`)
	assert.Contains(t, got, `description: "a \"b\" \\c"`)

	// A commented example is not treated as the real key; a new line is added.
	got = SetDescription("# description: example\nmode: static\n", "real")
	assert.Contains(t, got, `description: "real"`)
	assert.Contains(t, got, "# description: example")
}

func TestSetDescriptionIsLineSurgery(t *testing.T) {
	t.Parallel()
	// hostit.yml is the owner's file: everything around the description line --
	// comments, blank lines, indentation, key order -- must come through
	// byte-for-byte, which is why this is line surgery and not a YAML round-trip.
	doc := "# my app\nmode: app\nrun: ./server   # trailing comment\ndescription: old\n\nenv:\n  KEY: value\n"
	assert.Equal(t, "# my app\nmode: app\nrun: ./server   # trailing comment\ndescription: \"new\"\n\nenv:\n  KEY: value\n", SetDescription(doc, "new"))

	// Inserting prepends, leaving the existing document untouched below.
	assert.Equal(t, "description: \"added\"\nmode: static\n# comment\n", SetDescription("mode: static\n# comment\n", "added"))

	// A colon in the value is safe: the value is always double-quoted.
	assert.Equal(t, "description: \"key: value\"\n", SetDescription("", "key: value"))
}
