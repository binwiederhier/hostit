package app

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the Manager's file methods end to end -- the delegation to
// the homefs service, the real subvolume/files path resolution and the app-uid
// chown seam.
// The exhaustive containment suite (every escape, mode and listing edge case) lives
// with the service it belongs to, in homefs/service_test.go.

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
}

func TestReadmeRoundTrip(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// New apps get a skeleton with a README the agent can build on
	readme, err := m.Readme("blog")
	require.NoError(t, err)
	assert.Contains(t, readme, "blog")
	require.NoError(t, m.WriteReadme("blog", "# blog\n\nThe finance dashboard.\n"))
	readme, err = m.Readme("blog")
	require.NoError(t, err)
	assert.Contains(t, readme, "finance dashboard")
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

// TestSymlinksCannotEscapeTheAppHome re-checks the security boundary through the
// Manager, so the delegation to homefs cannot silently drop it: the app user owns
// their files dir (it is their container's home, writable over scp), so any file
// operation the daemon performs as root must refuse to follow a link out of it.
func TestSymlinksCannotEscapeTheAppHome(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	home := m.appFiles("blog").Path()

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

// TestFilesRefuseAHomeSymlinkedOutOfTheSubvolume covers the unified-layout
// attack surface end to end through the Manager: the tenant is root inside
// their container, whose rootfs IS the app subvolume, so they can replace home
// (or home/app) with an absolute symlink. Every daemon read of the files dir --
// the file API, the hostit.yml load, the agent's state breadcrumb -- must
// refuse to follow it; a naive os.OpenRoot of the joined path would happily
// root the daemon wherever the link points.
func TestFilesRefuseAHomeSymlinkedOutOfTheSubvolume(t *testing.T) {
	t.Parallel()
	for _, plant := range []string{"home", "home/app"} {
		t.Run(plant, func(t *testing.T) {
			t.Parallel()
			m, _, _ := newTestDeployManager(t)
			createTestApp(t, m, "blog")
			writeAppFile(t, m, "blog", "hostit.yml", "mode: static\n")
			outside := t.TempDir()
			// Bait everything the daemon reads: a config and a breadcrumb at the
			// link target must NOT be what the daemon sees.
			require.NoError(t, os.MkdirAll(filepath.Join(outside, "log"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(outside, "hostit.yml"), []byte("mode: static\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(outside, "log", "state"), []byte("running\n"), 0o644))
			subvol := m.appSubvolume("blog")
			require.NoError(t, os.RemoveAll(filepath.Join(subvol, plant)))
			require.NoError(t, os.Symlink(outside, filepath.Join(subvol, plant)))

			// The file API refuses reads and writes alike.
			require.Error(t, m.WriteFile("blog", "x.txt", []byte("x"), 0), "writes through a planted %s link must be refused", plant)
			_, err := m.ReadFile("blog", "hostit.yml")
			require.Error(t, err, "reads through a planted %s link must be refused", plant)

			// The config load refuses, so a deploy cannot be steered by a link.
			_, err = m.loadConfig("blog")
			require.Error(t, err, "loadConfig must refuse a planted %s link", plant)

			// The breadcrumb read refuses: the baited "running" never surfaces.
			state, startedAt := m.appProcessState("blog")
			assert.Empty(t, state, "the agent breadcrumb must not be read through a planted %s link", plant)
			assert.Zero(t, startedAt)

			// Nothing landed outside the subvolume.
			assert.NoFileExists(t, filepath.Join(outside, "x.txt"))
		})
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
