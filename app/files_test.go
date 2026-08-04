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
	require.NoError(t, m.WriteFile("blog", "index.html", []byte("<h1>hi</h1>")))
	b, err := m.ReadFile("blog", "index.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>hi</h1>", string(b))
	// Nested paths are created on the way
	require.NoError(t, m.WriteFile("blog", "static/css/site.css", []byte("body{}")))
	b, err = m.ReadFile("blog", "static/css/site.css")
	require.NoError(t, err)
	assert.Equal(t, "body{}", string(b))
}

func TestWriteFileRejectsEscapes(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	for _, path := range []string{"../evil", "../../etc/passwd", "/etc/passwd", "a/../../b", ""} {
		err := m.WriteFile("blog", path, []byte("x"))
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
	require.NoError(t, m.WriteFile("blog", "index.html", []byte("x")))
	require.NoError(t, m.WriteFile("blog", "static/site.css", []byte("y")))
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
