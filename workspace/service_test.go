package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
)

func TestWorkspaceImageTagFollowsItsContent(t *testing.T) {
	t.Parallel()
	// The image is built once and then found by tag. With a fixed tag, editing
	// the Containerfile -- adding vim, say -- would never rebuild it, and the
	// change would silently never reach any app.
	tag := ImageTag()
	assert.True(t, strings.HasPrefix(tag, imagePrefix+":"), "got %q", tag)
	assert.Equal(t, tag, ImageTag(), "the same content must give the same tag")
	assert.NotEqual(t, tag, imageTagFor("FROM scratch\n"), "different content must give a different tag")
}

func TestWorkspaceContainerfile(t *testing.T) {
	t.Parallel()
	// The workspace image must contain the bits that make ssh/scp/sftp/rsync work
	assert.Contains(t, containerfile, "openssh-sftp-server")
	assert.Contains(t, containerfile, "rsync")
	assert.Contains(t, containerfile, "FROM docker.io/library/debian")
	// The runtimes an app can build against out of the box: Go, Python, Node.js,
	// PHP, and sqlite3 so an owner can inspect a persistent app's database over SSH
	assert.Contains(t, containerfile, "golang-go")
	assert.Contains(t, containerfile, "python3")
	assert.Contains(t, containerfile, "nodejs npm")
	assert.Contains(t, containerfile, "php-cli")
	assert.Contains(t, containerfile, "sqlite3")
	// Shell niceties (ll/colors) written into a system-wide profile.d script
	assert.Contains(t, containerfile, "/etc/profile.d/hostit.sh")
	assert.Contains(t, containerfile, "base64 -d")
}

func TestEnsureImageBuildsOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	svc, fc, _, _ := newTestService(t)
	require.NoError(t, svc.EnsureImage())
	require.Equal(t, []string{ImageTag()}, fc.builds)
	// The build context holds the Containerfile the image is built from
	b, err := os.ReadFile(filepath.Join(svc.dataDir, workspaceSubDir, "Containerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "FROM docker.io/library/debian")
	// The image now exists, so a second call must not rebuild it
	require.NoError(t, svc.EnsureImage())
	assert.Equal(t, []string{ImageTag()}, fc.builds)
}

func TestPruneOldImagesKeepsCurrentAndPinnedTags(t *testing.T) {
	t.Parallel()
	svc, fc, _, s := newTestService(t)
	// An app pinned to an old tag keeps that tag alive: it cannot be rebuilt (its
	// Containerfile is gone), so pruning it would strand the app.
	require.NoError(t, s.AddApp(&store.App{Name: "blog", Port: 10000, ImageTag: "localhost/hostit-workspace:pinned1"}))
	current := ImageTag()
	fc.images[current] = true
	fc.images["localhost/hostit-workspace:pinned1"] = true
	fc.images["localhost/hostit-workspace:old1"] = true
	fc.images["docker.io/library/debian:stable-slim"] = true

	// Every Containerfile change leaves its predecessor behind, and a workspace
	// image is well over a gigabyte on a 24 GB disk
	svc.PruneOldImages()
	assert.Equal(t, []string{"localhost/hostit-workspace:old1"}, fc.removed)
	assert.NotContains(t, fc.removed, current, "the image in use must survive")
	assert.NotContains(t, fc.removed, "localhost/hostit-workspace:pinned1", "a pinned tag is not garbage")
	assert.NotContains(t, fc.removed, "docker.io/library/debian:stable-slim", "only hostit's own images are ours to remove")
}

// newTestService builds a Service on a fake container runtime, a fake command
// runner (for the btrfs and chown calls), a fresh store and temp dirs.
func newTestService(t *testing.T) (*Service, *fakeContainer, *fakeRunner, *store.Store) {
	t.Helper()
	fc := newFakeContainer()
	r := &fakeRunner{}
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return New(fc, s, btrfs.New(r), r, t.TempDir(), t.TempDir()), fc, r, s
}

// fakeContainer fakes the image-store and export half of podman.Interface:
// which tags exist, what was built, removed, created-from and exported. The
// remaining container-lifecycle methods are never called by this package and
// panic via the embedded nil Interface.
type fakeContainer struct {
	podman.Interface
	images      map[string]bool
	builds      []string
	removed     []string
	createdFrom []string
	exportedTo  []string
	removedCtrs []string
	mu          sync.Mutex // Protects all recorded fields
}

func newFakeContainer() *fakeContainer {
	return &fakeContainer{images: make(map[string]bool)}
}

func (f *fakeContainer) ImageExists(tag string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[tag]
}

func (f *fakeContainer) Build(tag, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds = append(f.builds, tag)
	f.images[tag] = true
	return nil
}

func (f *fakeContainer) Images() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tags := make([]string, 0, len(f.images))
	for tag := range f.images {
		tags = append(tags, tag)
	}
	return strings.Join(tags, "\n") + "\n", nil
}

func (f *fakeContainer) RemoveImage(image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, image)
	delete(f.images, image)
	return nil
}
