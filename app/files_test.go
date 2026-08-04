package app

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
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
	listing, err := m.ListFiles("blog", "")
	require.NoError(t, err)
	names := make([]string, 0, len(listing.Files))
	for _, f := range listing.Files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	assert.Contains(t, names, "static")
	// Neither hostit's state nor the shell dotfiles useradd copies from
	// /etc/skel belong in what an agent sees
	require.NoError(t, os.WriteFile(filepath.Join(m.appHome("blog"), ".bashrc"), []byte("x"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(m.appHome("blog"), ".ssh"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(m.appHome("blog"), ".ssh", "authorized_keys"), []byte("k"), 0600))
	listing, err = m.ListFiles("blog", "")
	require.NoError(t, err)
	names = names[:0]
	for _, f := range listing.Files {
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
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("description: Expense tracker for the finance team\nmode: static\n"), 0))
	assert.Equal(t, "Expense tracker for the finance team", m.Description("blog"))
}

func TestDescriptionSurvivesAnUnfinishedConfig(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// Mid-edit, hostit.yml often names no runnable mode yet. The description is
	// still what the app says it is, and the owner's prompt depends on it.
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("description: A half-written app\n"), 0))
	assert.Equal(t, "A half-written app", m.Description("blog"))
	// Two modes at once is invalid too, and still not a reason to forget
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("description: Two modes\nmode: static\nrun: ./x\n"), 0))
	assert.Equal(t, "Two modes", m.Description("blog"))
	// Unparseable YAML has no description to offer
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("description: [unclosed\n"), 0))
	assert.Empty(t, m.Description("blog"))
}

func TestDescriptionIgnoresAnAbsurdConfig(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// hostit.yml is caller-writable, and this read happens once per app on a
	// polled endpoint: a huge file must not be parsed at all
	huge := append([]byte("description: A tiny blog\n# "), bytes.Repeat([]byte("x"), maxConfigSize)...)
	require.NoError(t, m.WriteFile("blog", "hostit.yml", huge, 0))
	assert.Empty(t, m.Description("blog"))
}

func TestWriteFileFromStreamsAndRejectsOversize(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFileFrom("blog", "server", strings.NewReader("#!/bin/sh\n"), 0o755))
	stat, err := os.Stat(filepath.Join(m.appHome("blog"), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())

	// A body over the cap is refused, and nothing of it is kept: the whole point
	// is that the daemon never holds it, so it must not land on disk either
	big := io.LimitReader(neverEndingReader{}, maxUploadSize+1024)
	err = m.WriteFileFrom("blog", "huge.bin", big, 0)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = os.Stat(filepath.Join(m.appHome("blog"), "huge.bin"))
	assert.True(t, os.IsNotExist(err), "a rejected upload must leave no file behind")

	// A failed overwrite leaves the previous content alone
	require.NoError(t, m.WriteFileFrom("blog", "keep.txt", strings.NewReader("original"), 0))
	err = m.WriteFileFrom("blog", "keep.txt", io.LimitReader(neverEndingReader{}, maxUploadSize+1), 0)
	require.ErrorIs(t, err, ErrInvalid)
	b, err := os.ReadFile(filepath.Join(m.appHome("blog"), "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "original", string(b))
}

// neverEndingReader produces zeros forever, so a size cap can be tested without
// allocating the test's way to the limit
type neverEndingReader struct{}

func (neverEndingReader) Read(p []byte) (int, error) {
	return len(p), nil
}

// TestSymlinksCannotEscapeTheAppHome covers the whole class: the app user owns
// their home (it is bind-mounted into their container and writable over scp),
// so any file operation the daemon performs as root must refuse to follow a
// link out of it. Lexical path checks alone do not see these.
func TestSymlinksCannotEscapeTheAppHome(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	home := m.appHome("blog")

	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("root-only secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(home, "notes.txt")))

	// Reading through the link must not hand back the target
	b, err := m.ReadFile("blog", "notes.txt")
	require.Error(t, err, "reading through a symlink must fail")
	assert.NotContains(t, string(b), "root-only secret")

	// Writing may replace the link (that is safe and useful), but must never
	// write through it
	require.NoError(t, m.WriteFile("blog", "notes.txt", []byte("overwritten"), 0))
	kept, readErr := os.ReadFile(outside)
	require.NoError(t, readErr)
	assert.Equal(t, "root-only secret", string(kept), "the target must be untouched")
	stat, err := os.Lstat(filepath.Join(home, "notes.txt"))
	require.NoError(t, err)
	assert.Zero(t, stat.Mode()&os.ModeSymlink, "the link must have been replaced by a real file")

	// A tar entry needs no symlink of its own: a planted directory link is enough
	outDir := t.TempDir()
	require.NoError(t, os.Symlink(outDir, filepath.Join(home, "link")))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "escaped"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "link/escaped.txt", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	_, err = m.ExtractTar("blog", &buf)
	require.Error(t, err, "a tar entry must not land through a planted directory symlink")
	_, err = os.Stat(filepath.Join(outDir, "escaped.txt"))
	assert.True(t, os.IsNotExist(err), "nothing may be written outside the home")

	// Deleting through a link must not delete the target
	err = m.DeleteFile("blog", "notes.txt")
	if err == nil {
		_, statErr := os.Stat(outside)
		assert.NoError(t, statErr, "deleting a link must not delete its target")
	}
}

func TestProtectedPathsAreNotWritable(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// An agent token that could write .ssh/authorized_keys would grant itself
	// SSH into the container; .hostit/ is hostit's own state
	for _, rel := range []string{
		".ssh/authorized_keys", ".ssh", ".hostit/app.log", ".hostit",
		".config/x", ".local/share/x", ".cache/x",
	} {
		require.ErrorIs(t, m.WriteFile("blog", rel, []byte("x"), 0), ErrInvalid, "write %q", rel)
		_, err := m.ReadFile("blog", rel)
		require.ErrorIs(t, err, ErrInvalid, "read %q", rel)
		require.ErrorIs(t, m.DeleteFile("blog", rel), ErrInvalid, "delete %q", rel)
	}
	// A dotfile of the app's own is still the app's business
	require.NoError(t, m.WriteFile("blog", ".env", []byte("KEY=value"), 0))
}

func TestExtractTarRejectsSymlinkEntries(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "passwd", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777,
	}))
	require.NoError(t, tw.Close())
	_, err := m.ExtractTar("blog", &buf)
	require.ErrorIs(t, err, ErrInvalid, "an archive must not be able to plant a symlink")
	_, err = os.Lstat(filepath.Join(m.appHome("blog"), "passwd"))
	assert.True(t, os.IsNotExist(err))
}

func TestListFilesIsOneLevelAtATime(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.WriteFile("blog", "public/index.html", []byte("<h1>hi</h1>"), 0))
	require.NoError(t, m.WriteFile("blog", "public/css/site.css", []byte("body{}"), 0))
	require.NoError(t, m.WriteFile("blog", "hostit.yml", []byte("mode: static\n"), 0))

	// The whole tree was returned before, which is fine until an app has a
	// node_modules and the listing is thirty thousand entries of someone else's
	// code, built in memory, on the endpoint an agent calls first
	root, err := m.ListFiles("blog", "")
	require.NoError(t, err)
	names := map[string]FileType{}
	for _, f := range root.Files {
		names[f.Path] = f.Type
	}
	assert.Equal(t, FileTypeDir, names["public"])
	assert.Equal(t, FileTypeFile, names["hostit.yml"])
	assert.NotContains(t, names, "public/index.html", "a listing shows one level")
	assert.False(t, root.Truncated)

	// ...and you can walk into it
	sub, err := m.ListFiles("blog", "public")
	require.NoError(t, err)
	paths := make([]string, 0)
	for _, f := range sub.Files {
		paths = append(paths, f.Path)
	}
	assert.Contains(t, paths, "public/index.html")
	assert.Contains(t, paths, "public/css")

	// Dependency directories are noise an agent should not have to page through
	require.NoError(t, m.WriteFile("blog", "node_modules/left-pad/index.js", []byte("x"), 0))
	root, err = m.ListFiles("blog", "")
	require.NoError(t, err)
	for _, f := range root.Files {
		assert.NotEqual(t, "node_modules", f.Path, "dependency directories are skipped")
	}
	_, err = m.ListFiles("blog", "../etc")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestListFilesCapsAHugeDirectory(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	for i := 0; i < maxListEntries+50; i++ {
		require.NoError(t, m.WriteFile("blog", fmt.Sprintf("public/f%d.txt", i), []byte("x"), 0))
	}
	listing, err := m.ListFiles("blog", "public")
	require.NoError(t, err)
	assert.Len(t, listing.Files, maxListEntries)
	assert.True(t, listing.Truncated, "a caller must be told the listing is partial")
}
