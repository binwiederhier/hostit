package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFileMaxRefusesOversized(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "uploads/pic.bin", strings.Repeat("x", 1000))

	// Under the cap: read fully.
	b, err := m.ReadFileMax("blog", "uploads/pic.bin", 2000)
	require.NoError(t, err)
	assert.Len(t, b, 1000)

	// Over the cap: refused, not truncated (guards the shared daemon's memory).
	_, err = m.ReadFileMax("blog", "uploads/pic.bin", 500)
	assert.Error(t, err)
}

func TestFileExists(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "uploads/logo.png", "data")

	assert.True(t, m.FileExists("blog", "uploads/logo.png"))
	assert.False(t, m.FileExists("blog", "uploads/missing.png"))
	assert.False(t, m.FileExists("blog", "../../etc/passwd")) // traversal refused
}
