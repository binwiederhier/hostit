package homefs

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errInvalid is the sentinel the Service wraps for request-validation failures.
// A caller injects its own; the tests assert against this one to prove the
// wrapping contract holds across the New boundary.
var errInvalid = errors.New("invalid request")

// nopChown is the post-write chown a plain-filesystem test does not need
func nopChown(root *os.Root, rel string) error { return nil }

// recordingChown records every path handed to the post-write chown, so a test can
// assert that new parent directories are given to the app user, not just the file.
func recordingChown(seen *[]string) Chowner {
	return func(root *os.Root, rel string) error {
		*seen = append(*seen, rel)
		return nil
	}
}

func TestWriteFileFromGivesNewParentDirsToTheAppUser(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	var chowned []string
	require.NoError(t, s.WriteFile(home, "uploads/pics/logo.png", []byte("x"), 0, recordingChown(&chowned)))
	// Not just the file: MkdirAll creates uploads/ and uploads/pics/ as the daemon
	// (root), and a root-owned directory falls outside the container's uid map, so
	// the app sees it as nobody and cannot even read it.
	assert.Contains(t, chowned, "uploads")
	assert.Contains(t, chowned, "uploads/pics")
}

func TestExtractTarGivesNewParentDirsToTheAppUser(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "console.log(1)"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "public/assets/app.js", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	var chowned []string
	_, err = s.ExtractTar(home, &buf, recordingChown(&chowned))
	require.NoError(t, err)
	assert.Contains(t, chowned, "public")
	assert.Contains(t, chowned, "public/assets")
	assert.Contains(t, chowned, "public/assets/app.js")
}

// newTestService builds a Service and a files Dir shaped like production: a
// subvolume with the files directory at home/app inside it. The files dir is
// pre-created so tests can plant files and symlinks in it directly.
func newTestService(t *testing.T) (*Service, Dir) {
	t.Helper()
	d := Dir{Subvolume: t.TempDir(), Rel: "home/app"}
	require.NoError(t, os.MkdirAll(d.Path(), 0o755))
	return New(errInvalid), d
}

// TestFilesDirSymlinkCannotEscapeTheSubvolume covers the unified-layout attack:
// the tenant is root inside their container, whose rootfs IS the app subvolume,
// so they can replace home (or home/app) with a symlink. Opening the files dir
// as one plain host path would follow that link as the root daemon; the chained
// open (subvolume root first, files dir resolved inside it) must refuse an
// absolute link out of the subvolume.
func TestFilesDirSymlinkCannotEscapeTheSubvolume(t *testing.T) {
	t.Parallel()
	s := New(errInvalid)
	for _, plant := range []string{"home", "home/app"} {
		subvol, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(subvol, plant)), 0o755))
		require.NoError(t, os.Symlink(outside, filepath.Join(subvol, plant)))
		d := Dir{Subvolume: subvol, Rel: "home/app"}
		require.Error(t, s.WriteFile(d, "index.html", []byte("x"), 0, nopChown), "writes through a %s symlink must be refused", plant)
		_, err := s.ReadFile(d, "index.html")
		require.Error(t, err, "reads through a %s symlink must be refused", plant)
		entries, _ := os.ReadDir(outside)
		assert.Empty(t, entries, "nothing may land outside the subvolume via %s", plant)
	}
}

// A link that stays inside the subvolume may resolve (os.Root allows it), which
// is the accepted worst case: the tenant redirects their own file API into
// another directory of their OWN subvolume. Nothing may leave the subvolume.
func TestFilesDirSymlinkInsideTheSubvolumeIsSelfHarmOnly(t *testing.T) {
	t.Parallel()
	s := New(errInvalid)
	subvol := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(subvol, "elsewhere"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(subvol, "home"), 0o755))
	require.NoError(t, os.Symlink("../elsewhere", filepath.Join(subvol, "home", "app")))
	d := Dir{Subvolume: subvol, Rel: "home/app"}
	require.NoError(t, s.WriteFile(d, "index.html", []byte("x"), 0, nopChown))
	assert.FileExists(t, filepath.Join(subvol, "elsewhere", "index.html"), "the write stays inside the subvolume")
}

func TestWriteAndReadFile(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "index.html", []byte("<h1>hi</h1>"), 0, nopChown))
	b, err := s.ReadFile(home, "index.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>hi</h1>", string(b))
	// Nested paths are created on the way
	require.NoError(t, s.WriteFile(home, "static/css/site.css", []byte("body{}"), 0, nopChown))
	b, err = s.ReadFile(home, "static/css/site.css")
	require.NoError(t, err)
	assert.Equal(t, "body{}", string(b))
}

func TestWriteFileRejectsEscapes(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	for _, path := range []string{"../evil", "../../etc/passwd", "/etc/passwd", "a/../../b", ""} {
		err := s.WriteFile(home, path, []byte("x"), 0, nopChown)
		require.Error(t, err, "path %q must be rejected", path)
		assert.ErrorIs(t, err, errInvalid)
	}
	// Nothing escaped the app home
	_, err := os.Stat(filepath.Join(filepath.Dir(home.Path()), "evil"))
	assert.Error(t, err)
}

func TestReadFileRejectsEscapes(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	_, err := s.ReadFile(home, "../../etc/passwd")
	require.ErrorIs(t, err, errInvalid)
}

func TestListFiles(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "index.html", []byte("x"), 0, nopChown))
	require.NoError(t, s.WriteFile(home, "static/site.css", []byte("y"), 0, nopChown))
	listing, err := s.ListFiles(home, "")
	require.NoError(t, err)
	names := make([]string, 0, len(listing.Files))
	for _, f := range listing.Files {
		names = append(names, f.Path)
	}
	assert.Contains(t, names, "index.html")
	assert.Contains(t, names, "static")
	// Neither hostit's state nor the shell dotfiles useradd copies from
	// /etc/skel belong in what an agent sees
	require.NoError(t, os.WriteFile(filepath.Join(home.Path(), ".bashrc"), []byte("x"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(home.Path(), ".ssh"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home.Path(), ".ssh", "authorized_keys"), []byte("k"), 0600))
	listing, err = s.ListFiles(home, "")
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
	s, home := newTestService(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := map[string]string{"app.py": "print('hi')", "static/x.txt": "data"}
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	written, err := s.ExtractTar(home, &buf, nopChown)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app.py", "static/x.txt"}, written)
	b, err := s.ReadFile(home, "app.py")
	require.NoError(t, err)
	assert.Equal(t, "print('hi')", string(b))
}

func TestExtractTarRejectsEscapingEntries(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "pwned"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	_, err = s.ExtractTar(home, &buf, nopChown)
	require.ErrorIs(t, err, errInvalid)
	_, err = os.Stat(filepath.Join(filepath.Dir(home.Path()), "escape.txt"))
	assert.Error(t, err, "the entry must not have escaped the app home")
}

func TestWriteFileMode(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	// A binary or script must be able to arrive ready to run
	require.NoError(t, s.WriteFile(home, "server", []byte("#!/bin/sh\necho hi\n"), 0o755, nopChown))
	stat, err := os.Stat(filepath.Join(home.Path(), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())

	// The default is a plain file
	require.NoError(t, s.WriteFile(home, "page.html", []byte("<h1>hi</h1>"), 0, nopChown))
	stat, err = os.Stat(filepath.Join(home.Path(), "page.html"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), stat.Mode().Perm())

	// Overwriting an existing file still applies the new mode
	require.NoError(t, s.WriteFile(home, "server", []byte("#!/bin/sh\necho bye\n"), 0o700, nopChown))
	stat, err = os.Stat(filepath.Join(home.Path(), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), stat.Mode().Perm())

	// Group and world write are never granted, owner read/write always are
	require.NoError(t, s.WriteFile(home, "odd", []byte("x"), 0o777, nopChown))
	stat, err = os.Stat(filepath.Join(home.Path(), "odd"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())
}

func TestExtractTarKeepsEntryModes(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
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
	_, err := s.ExtractTar(home, &buf, nopChown)
	require.NoError(t, err)
	for _, e := range entries {
		stat, err := os.Stat(filepath.Join(home.Path(), e.name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(e.mode), stat.Mode().Perm(), "entry %s", e.name)
	}
}

func TestWriteFileFromStreamsAndRejectsOversize(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFileFrom(home, "server", strings.NewReader("#!/bin/sh\n"), 0o755, nopChown))
	stat, err := os.Stat(filepath.Join(home.Path(), "server"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), stat.Mode().Perm())

	// A body over the cap is refused, and nothing of it is kept: the whole point
	// is that the daemon never holds it, so it must not land on disk either
	big := io.LimitReader(neverEndingReader{}, maxUploadSize+1024)
	err = s.WriteFileFrom(home, "huge.bin", big, 0, nopChown)
	require.ErrorIs(t, err, errInvalid)
	_, err = os.Stat(filepath.Join(home.Path(), "huge.bin"))
	assert.True(t, os.IsNotExist(err), "a rejected upload must leave no file behind")

	// A failed overwrite leaves the previous content alone
	require.NoError(t, s.WriteFileFrom(home, "keep.txt", strings.NewReader("original"), 0, nopChown))
	err = s.WriteFileFrom(home, "keep.txt", io.LimitReader(neverEndingReader{}, maxUploadSize+1), 0, nopChown)
	require.ErrorIs(t, err, errInvalid)
	b, err := os.ReadFile(filepath.Join(home.Path(), "keep.txt"))
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
// their files dir (it is their container's home and writable over scp), so any
// file operation the daemon performs as root must refuse to follow a link out
// of it. Lexical path checks alone do not see these.
func TestSymlinksCannotEscapeTheAppHome(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)

	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("root-only secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(home.Path(), "notes.txt")))

	// Reading through the link must not hand back the target
	b, err := s.ReadFile(home, "notes.txt")
	require.Error(t, err, "reading through a symlink must fail")
	assert.NotContains(t, string(b), "root-only secret")

	// Writing may replace the link (that is safe and useful), but must never
	// write through it
	require.NoError(t, s.WriteFile(home, "notes.txt", []byte("overwritten"), 0, nopChown))
	kept, readErr := os.ReadFile(outside)
	require.NoError(t, readErr)
	assert.Equal(t, "root-only secret", string(kept), "the target must be untouched")
	stat, err := os.Lstat(filepath.Join(home.Path(), "notes.txt"))
	require.NoError(t, err)
	assert.Zero(t, stat.Mode()&os.ModeSymlink, "the link must have been replaced by a real file")

	// A tar entry needs no symlink of its own: a planted directory link is enough
	outDir := t.TempDir()
	require.NoError(t, os.Symlink(outDir, filepath.Join(home.Path(), "link")))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "escaped"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "link/escaped.txt", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	_, err = s.ExtractTar(home, &buf, nopChown)
	require.Error(t, err, "a tar entry must not land through a planted directory symlink")
	_, err = os.Stat(filepath.Join(outDir, "escaped.txt"))
	assert.True(t, os.IsNotExist(err), "nothing may be written outside the home")

	// Deleting through a link must not delete the target
	err = s.DeleteFile(home, "notes.txt")
	if err == nil {
		_, statErr := os.Stat(outside)
		assert.NoError(t, statErr, "deleting a link must not delete its target")
	}
}

func TestProtectedPathsAreNotWritable(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	// An agent token that could write .ssh/authorized_keys would grant itself
	// SSH into the container; .hostit/ is hostit's own state
	for _, rel := range []string{
		".ssh/authorized_keys", ".ssh", ".hostit/app.log", ".hostit",
		".config/x", ".local/share/x", ".cache/x",
	} {
		require.ErrorIs(t, s.WriteFile(home, rel, []byte("x"), 0, nopChown), errInvalid, "write %q", rel)
		_, err := s.ReadFile(home, rel)
		require.ErrorIs(t, err, errInvalid, "read %q", rel)
		require.ErrorIs(t, s.DeleteFile(home, rel), errInvalid, "delete %q", rel)
	}
	// A dotfile of the app's own is still the app's business
	require.NoError(t, s.WriteFile(home, ".env", []byte("KEY=value"), 0, nopChown))
}

func TestExtractTarRejectsSymlinkEntries(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "passwd", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777,
	}))
	require.NoError(t, tw.Close())
	_, err := s.ExtractTar(home, &buf, nopChown)
	require.ErrorIs(t, err, errInvalid, "an archive must not be able to plant a symlink")
	_, err = os.Lstat(filepath.Join(home.Path(), "passwd"))
	assert.True(t, os.IsNotExist(err))
}

func TestListFilesIsOneLevelAtATime(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "public/index.html", []byte("<h1>hi</h1>"), 0, nopChown))
	require.NoError(t, s.WriteFile(home, "public/css/site.css", []byte("body{}"), 0, nopChown))
	require.NoError(t, s.WriteFile(home, "hostit.yml", []byte("mode: static\n"), 0, nopChown))

	// The whole tree was returned before, which is fine until an app has a
	// node_modules and the listing is thirty thousand entries of someone else's
	// code, built in memory, on the endpoint an agent calls first
	root, err := s.ListFiles(home, "")
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
	sub, err := s.ListFiles(home, "public")
	require.NoError(t, err)
	paths := make([]string, 0)
	for _, f := range sub.Files {
		paths = append(paths, f.Path)
	}
	assert.Contains(t, paths, "public/index.html")
	assert.Contains(t, paths, "public/css")

	// Dependency directories are noise an agent should not have to page through
	require.NoError(t, s.WriteFile(home, "node_modules/left-pad/index.js", []byte("x"), 0, nopChown))
	root, err = s.ListFiles(home, "")
	require.NoError(t, err)
	for _, f := range root.Files {
		assert.NotEqual(t, "node_modules", f.Path, "dependency directories are skipped")
	}
	_, err = s.ListFiles(home, "../etc")
	require.ErrorIs(t, err, errInvalid)
}

func TestListFilesCapsAHugeDirectory(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	for i := 0; i < maxListEntries+50; i++ {
		require.NoError(t, s.WriteFile(home, fmt.Sprintf("public/f%d.txt", i), []byte("x"), 0, nopChown))
	}
	listing, err := s.ListFiles(home, "public")
	require.NoError(t, err)
	assert.Len(t, listing.Files, maxListEntries)
	assert.True(t, listing.Truncated, "a caller must be told the listing is partial")
}

func TestReadFileMaxRefusesOversized(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "uploads/pic.bin", []byte(strings.Repeat("x", 1000)), 0, nopChown))

	// Under the cap: read fully.
	b, err := s.ReadFileMax(home, "uploads/pic.bin", 2000)
	require.NoError(t, err)
	assert.Len(t, b, 1000)

	// Over the cap: refused, not truncated (guards the shared daemon's memory).
	_, err = s.ReadFileMax(home, "uploads/pic.bin", 500)
	assert.Error(t, err)
}

func TestFileExists(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "uploads/logo.png", []byte("data"), 0, nopChown))

	assert.True(t, s.FileExists(home, "uploads/logo.png"))
	assert.False(t, s.FileExists(home, "uploads/missing.png"))
	assert.False(t, s.FileExists(home, "../../etc/passwd")) // traversal refused
}

func TestMoveFile(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "a.txt", []byte("hi"), 0, nopChown))

	require.NoError(t, s.MoveFile(home, "a.txt", "public/b.txt"))
	assert.False(t, s.FileExists(home, "a.txt"))
	b, err := s.ReadFile(home, "public/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "hi", string(b))

	// Refuse to clobber an existing destination.
	require.NoError(t, s.WriteFile(home, "c.txt", []byte("x"), 0, nopChown))
	assert.Error(t, s.MoveFile(home, "c.txt", "public/b.txt"))
	// Traversal out of the home is refused.
	assert.Error(t, s.MoveFile(home, "public/b.txt", "../escape.txt"))
}

func TestMakeDir(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)

	require.NoError(t, s.MakeDir(home, "assets/img", nopChown))
	info, err := s.StatFile(home, "assets/img")
	require.NoError(t, err)
	assert.Equal(t, FileTypeDir, info.Type)

	// Refuse to create over an existing path, and refuse traversal.
	assert.Error(t, s.MakeDir(home, "assets/img", nopChown))
	assert.Error(t, s.MakeDir(home, "../escape", nopChown))
}

func TestStatFile(t *testing.T) {
	t.Parallel()
	s, home := newTestService(t)
	require.NoError(t, s.WriteFile(home, "notes.txt", []byte("just some text"), 0, nopChown))
	require.NoError(t, s.WriteFile(home, "public/logo.png", []byte("\x89PNG\r\n\x1a\n"), 0, nopChown)) // PNG magic
	require.NoError(t, s.WriteFile(home, "data", []byte("\x00\x01\x02binary\x00"), 0, nopChown))       // no extension, binary content

	// A text file: size + modtime from a stat, and a text/* MIME (no full read).
	info, err := s.StatFile(home, "notes.txt")
	require.NoError(t, err)
	assert.Equal(t, FileTypeFile, info.Type)
	assert.Equal(t, int64(14), info.Size)
	assert.False(t, info.Modified.IsZero())
	assert.True(t, strings.HasPrefix(info.Mime, "text/"), "want text/* mime, got %q", info.Mime)

	// A known-image extension: MIME by extension, no sniff needed.
	info, err = s.StatFile(home, "public/logo.png")
	require.NoError(t, err)
	assert.Equal(t, "image/png", info.Mime)

	// No extension + binary bytes: sniffed as a non-text type.
	info, err = s.StatFile(home, "data")
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(info.Mime, "text/"), "want binary mime, got %q", info.Mime)

	// A directory reports as a dir with no MIME, and traversal is refused.
	info, err = s.StatFile(home, "public")
	require.NoError(t, err)
	assert.Equal(t, FileTypeDir, info.Type)
	_, err = s.StatFile(home, "../../etc/passwd")
	assert.Error(t, err)
}
