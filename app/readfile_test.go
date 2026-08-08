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

func TestMoveFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "a.txt", "hi")

	require.NoError(t, m.MoveFile("blog", "a.txt", "public/b.txt"))
	assert.False(t, m.FileExists("blog", "a.txt"))
	b, err := m.ReadFile("blog", "public/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "hi", string(b))

	// Refuse to clobber an existing destination.
	writeAppFile(t, m, "blog", "c.txt", "x")
	assert.Error(t, m.MoveFile("blog", "c.txt", "public/b.txt"))
	// Traversal out of the home is refused.
	assert.Error(t, m.MoveFile("blog", "public/b.txt", "../escape.txt"))
}

func TestMakeDir(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")

	require.NoError(t, m.MakeDir("blog", "assets/img"))
	info, err := m.StatFile("blog", "assets/img")
	require.NoError(t, err)
	assert.Equal(t, FileTypeDir, info.Type)

	// Refuse to create over an existing path, and refuse traversal.
	assert.Error(t, m.MakeDir("blog", "assets/img"))
	assert.Error(t, m.MakeDir("blog", "../escape"))
}

func TestStatFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "notes.txt", "just some text")
	writeAppFile(t, m, "blog", "public/logo.png", "\x89PNG\r\n\x1a\n") // PNG magic
	writeAppFile(t, m, "blog", "data", "\x00\x01\x02binary\x00")       // no extension, binary content

	// A text file: size + modtime from a stat, and a text/* MIME (no full read).
	info, err := m.StatFile("blog", "notes.txt")
	require.NoError(t, err)
	assert.Equal(t, FileTypeFile, info.Type)
	assert.Equal(t, int64(14), info.Size)
	assert.False(t, info.Modified.IsZero())
	assert.True(t, strings.HasPrefix(info.Mime, "text/"), "want text/* mime, got %q", info.Mime)

	// A known-image extension: MIME by extension, no sniff needed.
	info, err = m.StatFile("blog", "public/logo.png")
	require.NoError(t, err)
	assert.Equal(t, "image/png", info.Mime)

	// No extension + binary bytes: sniffed as a non-text type.
	info, err = m.StatFile("blog", "data")
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(info.Mime, "text/"), "want binary mime, got %q", info.Mime)

	// A directory reports as a dir with no MIME, and traversal is refused.
	info, err = m.StatFile("blog", "public")
	require.NoError(t, err)
	assert.Equal(t, FileTypeDir, info.Type)
	_, err = m.StatFile("blog", "../../etc/passwd")
	assert.Error(t, err)
}
