package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
)

func TestBaseAndRootfsPaths(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	// The base directory is named by the tag's content hash (the part after the
	// colon); the full tag has "/" and ":" in it and cannot be a directory name.
	base := svc.BasePath("localhost/hostit-workspace:abc123")
	assert.Equal(t, filepath.Join(svc.appsDir, ".bases", "abc123"), base)
	assert.Equal(t, filepath.Join(svc.appsDir, ".rootfs", "appid123"), svc.RootfsPath("appid123"))
}

func TestEnsureBaseExportsOnceAndSealsReadOnly(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	fc.images[ImageTag()] = true
	require.NoError(t, svc.EnsureBase(ImageTag()))

	base := svc.BasePath(ImageTag())
	// The export recipe: subvolume, create a throwaway container, stream its
	// filesystem out, remove it, then seal the base so nothing can dirty what
	// every app rootfs shares.
	assert.Contains(t, r.ran(), "btrfs subvolume create "+base)
	assert.Equal(t, []string{ImageTag()}, fc.createdFrom)
	assert.Equal(t, []string{base}, fc.exportedTo)
	assert.Equal(t, []string{"ctr-1"}, fc.removedCtrs)
	assert.Contains(t, r.ran(), "btrfs property set "+base+" ro true")
	assert.Empty(t, fc.builds, "an existing image is not rebuilt")

	// The base exists now, so a second call must not export again.
	require.NoError(t, svc.EnsureBase(ImageTag()))
	assert.Equal(t, []string{base}, fc.exportedTo)
}

func TestEnsureBaseBuildsTheImageFirst(t *testing.T) {
	t.Parallel()
	svc, fc, _, _ := newTestService(t)
	// No image at all: the current tag is built (the image remains the build
	// input), then exported into the base subvolume.
	require.NoError(t, svc.EnsureBase(ImageTag()))
	assert.Equal(t, []string{ImageTag()}, fc.builds)
	assert.Equal(t, []string{svc.BasePath(ImageTag())}, fc.exportedTo)
}

func TestEnsureBaseRefusesAnUnbuildableOldTag(t *testing.T) {
	t.Parallel()
	svc, fc, _, _ := newTestService(t)
	// An old pinned tag whose image is gone cannot be exported: only the current
	// Containerfile can be built, and silently exporting the wrong image would
	// hand the app a rootfs it was never pinned to.
	err := svc.EnsureBase(imagePrefix + ":gonetag")
	require.Error(t, err)
	assert.Equal(t, []string{ImageTag()}, fc.builds, "the ensure still built the current image (the only one it can)")
	assert.Empty(t, fc.exportedTo)
}

func TestEnsureRootfsSnapshotsThePinnedBaseAndChowns(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	// The app is pinned to an old tag whose base already exists: no image build,
	// no export -- just a snapshot of that base, chowned to the app's id block.
	pinned := imagePrefix + ":pinned1"
	require.NoError(t, os.MkdirAll(svc.BasePath(pinned), 0o700))
	a := &store.App{ID: "appid123", Name: "blog", ImageTag: pinned}
	require.NoError(t, svc.EnsureRootfs(a, IDs{UID: 1001, GID: 1001, Count: 65536}))

	rootfs := svc.RootfsPath("appid123")
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+svc.BasePath(pinned)+" "+rootfs)
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot -r", "the rootfs is writable")
	assert.Contains(t, r.ran(), "chown -R 1001:1001 "+rootfs)
	assert.Empty(t, fc.builds)
	assert.Empty(t, fc.exportedTo)
}

func TestEnsureRootfsFallsBackToTheCurrentTagWhenUnpinned(t *testing.T) {
	t.Parallel()
	svc, _, r, _ := newTestService(t)
	// An app from before pinning (empty tag) ran the current image, so its rootfs
	// snapshots the CURRENT base -- never ".bases/" itself (the empty dir name).
	require.NoError(t, os.MkdirAll(svc.BasePath(ImageTag()), 0o700))
	a := &store.App{ID: "appid123", Name: "blog"}
	require.NoError(t, svc.EnsureRootfs(a, IDs{UID: 1001, GID: 1001, Count: 65536}))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+svc.BasePath(ImageTag())+" "+svc.RootfsPath("appid123"))
	assert.NotContains(t, r.ran(), "snapshot "+filepath.Join(svc.appsDir, basesDirName)+" ")
}

func TestEnsureRootfsNeverRecreatesAnExistingRootfs(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	// THE invariant: an app's rootfs, once created, is never recreated or reset.
	// Even with its base missing entirely, an existing rootfs must be left alone
	// -- everything the app installed lives there.
	require.NoError(t, os.MkdirAll(svc.RootfsPath("appid123"), 0o700))
	a := &store.App{ID: "appid123", Name: "blog", ImageTag: imagePrefix + ":whatever"}
	require.NoError(t, svc.EnsureRootfs(a, IDs{UID: 1001, GID: 1001, Count: 65536}))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot")
	assert.NotContains(t, r.ran(), "chown")
	assert.Empty(t, fc.builds)
	assert.Empty(t, fc.exportedTo)
}

func TestForkRootfsSnapshotsTheSourceRootfs(t *testing.T) {
	t.Parallel()
	svc, _, r, _ := newTestService(t)
	// A fork carries the source's installed packages, so it snapshots the SOURCE
	// rootfs, not the base.
	require.NoError(t, os.MkdirAll(svc.RootfsPath("srcid"), 0o700))
	require.NoError(t, svc.ForkRootfs("srcid", "dstid", IDs{UID: 2001, GID: 2001, Count: 65536}))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+svc.RootfsPath("srcid")+" "+svc.RootfsPath("dstid"))
	assert.Contains(t, r.ran(), "chown -R 2001:2001 "+svc.RootfsPath("dstid"))
}

func TestForkRootfsRequiresTheSource(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	require.Error(t, svc.ForkRootfs("missing", "dstid", IDs{UID: 2001, GID: 2001}))
}

func TestDeleteRootfsRemovesTheSubvolume(t *testing.T) {
	t.Parallel()
	svc, _, r, _ := newTestService(t)
	require.NoError(t, svc.DeleteRootfs("appid123"))
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+svc.RootfsPath("appid123"))
}

func TestRootfsIDsListsExistingRootfsSubvolumes(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	require.NoError(t, os.MkdirAll(svc.RootfsPath("one"), 0o700))
	require.NoError(t, os.MkdirAll(svc.RootfsPath("two"), 0o700))
	ids, err := svc.RootfsIDs()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"one", "two"}, ids)
}

func TestPruneOldBasesRefusesPinnedAndCurrentTags(t *testing.T) {
	t.Parallel()
	svc, _, r, st := newTestService(t)
	// A pinned base may never be deleted: its extents are shared with every app
	// pinned to it, and deleting it would turn them into each app's EXCLUSIVE
	// bytes -- silently charging their budgets.
	pinned := imagePrefix + ":pinned1"
	require.NoError(t, st.AddApp(&store.App{Name: "blog", Port: 10000, ImageTag: pinned}))
	require.NoError(t, os.MkdirAll(svc.BasePath(ImageTag()), 0o700))
	require.NoError(t, os.MkdirAll(svc.BasePath(pinned), 0o700))
	require.NoError(t, os.MkdirAll(svc.BasePath(imagePrefix+":old1"), 0o700))

	svc.PruneOldBases()
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+svc.BasePath(imagePrefix+":old1"))
	assert.NotContains(t, r.ran(), "btrfs subvolume delete "+svc.BasePath(ImageTag()))
	assert.NotContains(t, r.ran(), "btrfs subvolume delete "+svc.BasePath(pinned))
}

// fakeRunner records commands and, like the real btrfs tool, materializes the
// destination directory of a subvolume create/snapshot so ensure-style checks
// (os.Stat on the base/rootfs path) see the effect.
type fakeRunner struct {
	commands []string
	mu       sync.Mutex // Protects commands; EnsureBase may run from goroutines
}

var _ run.Runner = (*fakeRunner)(nil)

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, strings.Join(args, " "))
	if len(args) >= 4 && args[0] == "btrfs" && args[1] == "subvolume" &&
		(args[2] == "create" || args[2] == "snapshot") {
		_ = os.MkdirAll(args[len(args)-1], 0o755)
	}
	return "", nil
}

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	return f.Run(args...)
}

func (f *fakeRunner) ran() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.commands, "\n")
}

// fakeContainer additions for the rootfs lifecycle: record what was created from
// an image and where a container filesystem was exported to.
func (f *fakeContainer) CreateFrom(image string, _ ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdFrom = append(f.createdFrom, image)
	return fmt.Sprintf("ctr-%d", len(f.createdFrom)), nil
}

func (f *fakeContainer) ExportRootfs(_ time.Duration, _, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exportedTo = append(f.exportedTo, dir)
	return nil
}

func (f *fakeContainer) RemoveForce(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedCtrs = append(f.removedCtrs, name)
	return nil
}
