package util

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderDrawsHeadersAndCells(t *testing.T) {
	t.Parallel()
	out := Render([]string{"NAME", "PORT"}, [][]string{{"blog", "10000"}, {"shop", "10001"}})
	for _, want := range []string{"NAME", "PORT", "blog", "10000", "shop", "10001"} {
		assert.Contains(t, out, want)
	}
	require.True(t, strings.Index(out, "NAME") < strings.Index(out, "blog"), "the header row comes first")
	// The header is visually separated from the rows, not just another row.
	assert.Greater(t, strings.Count(out, "\n"), 3, "borders add structure beyond the bare rows")
}

func TestRenderEmptyTableIsJustTheHeader(t *testing.T) {
	t.Parallel()
	out := Render([]string{"NAME"}, nil)
	assert.Contains(t, out, "NAME")
}

func TestTitle(t *testing.T) {
	t.Parallel()
	assert.Contains(t, Title("NODES (2)"), "NODES (2)")
}
