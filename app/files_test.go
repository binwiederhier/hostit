package app

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndReadFile(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "index.html", []byte("<h1>hi</h1>"), 0))
	b, err := m.ReadFile("blog", "index.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>hi</h1>", string(b))
	// Nested paths are created on the way
	require.NoError(t, m.WriteFile("blog", "static/css/site.css", []byte("body{}"), 0))
	b, err = m.ReadFile("blog", "static/css/site.css")
	require.NoError(t, err)
	assert.Equal(t, "body{}", string(b))
}

func TestWriteFileRejectsEscapes(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	for _, path := range []string{"../evil", "../../etc/passwd", "/etc/passwd", "a/../../b", ""} {
		err := m.WriteFile("blog", path, []byte("x"), 0)
		require.Error(t, err, "path %q must be rejected", path)
		assert.ErrorIs(t, err, ErrInvalid)
	}
	// Nothing escaped the app home
	_, err := os.Stat(filepath.Join(filepath.Dir(m.appHome("blog")), "evil"))
	assert.Error(t, err)
}

func TestReadFileRejectsEscapes(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	_, err := m.ReadFile("blog", "../../etc/passwd")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestListFiles(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "index.html", []byte("x"), 0))
	require.NoError(t, m.WriteFile("blog", "static/site.css", []byte("y"), 0))
	files, err := m.ListFiles("blog")
	require.NoError(t, err)
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	assert.Contains(t, names, "static/site.css")
	// Neither hostit's state nor the shell dotfiles useradd copies from
	// /etc/skel belong in what an agent sees
	require.NoError(t, os.WriteFile(filepath.Join(m.appHome("blog"), ".bashrc"), []byte("x"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(m.appHome("blog"), ".ssh"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(m.appHome("blog"), ".ssh", "authorized_keys"), []byte("k"), 0600))
	files, err = m.ListFiles("blog")
	require.NoError(t, err)
	names = names[:0]
	for _, f := range files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	for _, n := range names {
		assert.False(t, strings.HasPrefix(n, "."), "hidden entries must not be listed: %s", n)
	}
}

func TestExtractTar(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := map[string]string{"app.py": "print('hi')", "static/x.txt": "data"}
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	written, err := m.ExtractTar("blog", &buf)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app.py", "static/x.txt"}, written)
	b, err := m.ReadFile("blog", "app.py")
	require.NoError(t, err)
	assert.Equal(t, "print('hi')", string(b))
}

func TestExtractTarRejectsEscapingEntries(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "pwned"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	_, err = m.ExtractTar("blog", &buf)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = os.Stat(filepath.Join(filepath.Dir(m.appHome("blog")), "escape.txt"))
	assert.Error(t, err, "the entry must not have escaped the app home")
}

func TestReadmeRoundTrip(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// New apps are scaffolded with a README the agent can build on
	readme, err := m.Readme("blog")
	require.NoError(t, err)
	assert.Contains(t, readme, "blog")
	require.NoError(t, m.WriteReadme("blog", "# blog\n\nThe finance dashboard.\n"))
	readme, err = m.Readme("blog")
	require.NoError(t, err)
	assert.Contains(t, readme, "finance dashboard")
}

func TestWriteFileMode(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// A binary or script must be able to arrive ready to run
	require.NoError(t, m.WriteFile("blog", "server", []byte("#!/bin/sh\necho hi\n"), 0o755))
	stat, err := os.Stat(filepath.Join(m.appHome("blog"), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())

	// The default is a plain file
	require.NoError(t, m.WriteFile("blog", "page.html", []byte("<h1>hi</h1>"), 0))
	stat, err = os.Stat(filepath.Join(m.appHome("blog"), "page.html"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), stat.Mode().Perm())

	// Overwriting an existing file still applies the new mode
	require.NoError(t, m.WriteFile("blog", "server", []byte("#!/bin/sh\necho bye\n"), 0o700))
	stat, err = os.Stat(filepath.Join(m.appHome("blog"), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), stat.Mode().Perm())

	// Group and world write are never granted, owner read/write always are
	require.NoError(t, m.WriteFile("blog", "odd", []byte("x"), 0o777))
	stat, err = os.Stat(filepath.Join(m.appHome("blog"), "odd"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())
}

func TestExtractTarKeepsEntryModes(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		name string
		mode int64
	}{{"run.sh", 0o755}, {"data.txt", 0o644}}
	for _, e := range entries {
		content := "x"
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	_, err := m.ExtractTar("blog", &buf)
	require.NoError(t, err)
	for _, e := range entries {
		stat, err := os.Stat(filepath.Join(m.appHome("blog"), e.name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(e.mode), stat.Mode().Perm(), "entry %s", e.name)
	}
}

func TestDescriptionFromHostitYml(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// A stub has none; whoever builds the app fills it in
	assert.Empty(t, m.Description("blog"))
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("description: Expense tracker for the finance team\nstatic: .\n"), 0))
	assert.Equal(t, "Expense tracker for the finance team", m.Description("blog"))
}
