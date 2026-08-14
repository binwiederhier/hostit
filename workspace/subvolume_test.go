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

func TestBaseAndAppSubvolumePaths(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	// The base directory is named by the tag's content hash (the part after the
	// colon); the full tag has "/" and ":" in it and cannot be a directory name.
	base := svc.BasePath("localhost/hostit-workspace:abc123")
	assert.Equal(t, filepath.Join(svc.appsDir, ".bases", "abc123"), base)
	// An app IS one subvolume, directly under the apps dir, keyed on its id.
	assert.Equal(t, filepath.Join(svc.appsDir, "appid123"), svc.AppSubvolumePath("appid123"))
}

func TestEnsureBaseExportsOnceAndSealsReadOnly(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	fc.images[ImageTag()] = true
	require.NoError(t, svc.EnsureBase(ImageTag()))

	base := svc.BasePath(ImageTag())
	tmp := base + ".tmp"
	// The export recipe runs entirely against a TEMP subvolume -- create a
	// throwaway container, stream its filesystem out, seal it read-only -- and only
	// a rename publishes it at the base path. The daemon dying mid-export (a deploy
	// restart did exactly this on stage) must never leave a half-written base that
	// passes the exists check: partial bases gave every app a rootfs without its
	// ELF interpreter.
	assert.Contains(t, r.ran(), "btrfs subvolume create "+tmp)
	assert.Equal(t, []string{ImageTag()}, fc.createdFrom)
	assert.Equal(t, []string{tmp}, fc.exportedTo)
	assert.Equal(t, []string{"ctr-1"}, fc.removedCtrs)
	assert.Contains(t, r.ran(), "btrfs property set "+tmp+" ro true")
	assert.Empty(t, fc.builds, "an existing image is not rebuilt")
	// Published atomically: the base exists, the temp name is gone.
	assert.DirExists(t, base)
	assert.NoDirExists(t, tmp)

	// The base exists now, so a second call must not export again.
	require.NoError(t, svc.EnsureBase(ImageTag()))
	assert.Equal(t, []string{tmp}, fc.exportedTo)
}

func TestEnsureBaseCleansUpAKilledExport(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	fc.images[ImageTag()] = true
	base := svc.BasePath(ImageTag())
	tmp := base + ".tmp"
	// Simulate a daemon killed mid-export: the temp subvolume exists, the base
	// does not. The stale temp must be discarded and a fresh export published.
	require.NoError(t, os.MkdirAll(tmp, 0o755))
	require.NoError(t, svc.EnsureBase(ImageTag()))
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+tmp)
	assert.Equal(t, []string{tmp}, fc.exportedTo, "the export must run again from scratch")
	assert.DirExists(t, base)
	assert.NoDirExists(t, tmp)
}

func TestEnsureBaseBuildsTheImageFirst(t *testing.T) {
	t.Parallel()
	svc, fc, _, _ := newTestService(t)
	// No image at all: the current tag is built (the image remains the build
	// input), then exported into the base subvolume (via its temp path).
	require.NoError(t, svc.EnsureBase(ImageTag()))
	assert.Equal(t, []string{ImageTag()}, fc.builds)
	assert.Equal(t, []string{svc.BasePath(ImageTag()) + ".tmp"}, fc.exportedTo)
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

func TestEnsureAppSubvolumeSnapshotsThePinnedBase(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	// The app is pinned to an old tag whose base already exists: no image build,
	// no export -- just a snapshot of that base. NO chown: the subvolume stays
	// root-owned, and the container idmap-mounts it (instant creation).
	pinned := imagePrefix + ":pinned1"
	require.NoError(t, os.MkdirAll(svc.BasePath(pinned), 0o700))
	a := &store.App{ID: "appid123", Name: "blog", ImageTag: pinned}
	require.NoError(t, svc.EnsureAppSubvolume(a))

	subvol := svc.AppSubvolumePath("appid123")
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+svc.BasePath(pinned)+" "+subvol)
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot -r", "the app subvolume is writable")
	assert.NotContains(t, r.ran(), "chown", "the subvolume stays root-owned for the idmap mount")
	// The files directory exists inside the subvolume (the base may not ship
	// /home/app), traversable so sshd can reach .ssh as the app user.
	assert.DirExists(t, filepath.Join(subvol, FilesDir))
	assert.Empty(t, fc.builds)
	assert.Empty(t, fc.exportedTo)
}

func TestEnsureBaseRefusesAnEmptyTag(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	// Every app row has carried a pinned tag since the (now-removed) storage
	// migrations backfilled them; an empty tag today means a hand-edited
	// registry, and silently building "the current" base would hand the app a
	// rootfs it was never pinned to.
	err := svc.EnsureBase("")
	require.Error(t, err)
}

func TestEnsureAppSubvolumeNeverRecreatesAnExistingOne(t *testing.T) {
	t.Parallel()
	svc, fc, r, _ := newTestService(t)
	// THE invariant: an app's subvolume, once created, is never recreated or
	// reset. Even with its base missing entirely, an existing subvolume must be
	// left alone -- the app's files AND everything it installed live there.
	require.NoError(t, os.MkdirAll(svc.AppSubvolumePath("appid123"), 0o700))
	a := &store.App{ID: "appid123", Name: "blog", ImageTag: imagePrefix + ":whatever"}
	require.NoError(t, svc.EnsureAppSubvolume(a))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot")
	assert.NotContains(t, r.ran(), "chown")
	assert.Empty(t, fc.builds)
	assert.Empty(t, fc.exportedTo)
}

func TestForkAppSubvolumeSnapshotsTheSeedPath(t *testing.T) {
	t.Parallel()
	svc, _, r, _ := newTestService(t)
	// A fork snapshots the seed subvolume -- the source app's subvolume or a
	// whole-app snapshot -- so it carries files AND installed packages in one
	// CoW copy; ownership is untouched (root-owned, idmap-mounted).
	src := svc.AppSubvolumePath("srcid")
	require.NoError(t, os.MkdirAll(src, 0o700))
	require.NoError(t, svc.ForkAppSubvolume(src, "dstid"))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+src+" "+svc.AppSubvolumePath("dstid"))
	assert.NotContains(t, r.ran(), "chown", "a fork stays root-owned like every subvolume")
}

func TestForkAppSubvolumeRequiresTheSeed(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newTestService(t)
	require.Error(t, svc.ForkAppSubvolume(svc.AppSubvolumePath("missing"), "dstid"))
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
	// The base publish is a rename; emulate it so the atomicity assertions
	// (base exists, temp gone) run against the real temp dir.
	if len(args) == 3 && args[0] == "mv" {
		_ = os.Rename(args[1], args[2])
	}
	return "", nil
}

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	return f.Run(args...)
}

func (f *fakeRunner) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = nil
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
