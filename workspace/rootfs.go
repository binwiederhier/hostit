package workspace

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"heckel.io/hostit/store"
)

const (
	// basesDirName holds the read-only base rootfs subvolumes, one per image tag,
	// and rootfsDirName the per-app writable rootfs subvolumes. Both are hidden
	// dirs beside the app homes under the apps pool (like .snapshots), so
	// everything tenant-writable shares one qgroup-capable filesystem.
	basesDirName  = ".bases"
	rootfsDirName = ".rootfs"
	// hiddenDirMode keeps .bases/.rootfs root-only; apps reach their rootfs through
	// the container runtime, never by path.
	hiddenDirMode = 0o700
	// exportTimeout bounds a base export (podman export | tar). Measured at ~40s
	// for the current ~860 MB image; generous headroom for a loaded small host.
	exportTimeout = 15 * time.Minute
)

// BasePath is the base subvolume for an image tag. The directory is named by the
// tag's content hash (the part after the colon); the full tag has "/" and ":" in
// it and cannot be a path element.
func (s *Service) BasePath(tag string) string {
	return filepath.Join(s.appsDir, basesDirName, baseDirName(tag))
}

// RootfsPath is an app's writable rootfs subvolume, keyed on its stable id like
// the home, so a rename never moves it.
func (s *Service) RootfsPath(id string) string {
	return filepath.Join(s.appsDir, rootfsDirName, id)
}

// EnsureBase makes sure the read-only base subvolume for an image tag exists,
// building the image and exporting its filesystem if needed. The export is the
// one-time cost per tag (~40s); every app rootfs is then an instant snapshot of
// it. Serialized with the image-build mutex: two exports at once would thrash a
// small host the same way two builds would.
func (s *Service) EnsureBase(tag string) error {
	if tag == "" {
		tag = ImageTag() // an app from before pinning falls back to the current image
	}
	base := s.BasePath(tag)
	if _, err := os.Stat(base); err == nil {
		return nil
	}
	// Make sure the image to export exists. Only the current Containerfile can be
	// built, so a missing OLD tag is unrecoverable -- and silently exporting the
	// wrong image would hand pinned apps a rootfs they were never pinned to.
	if !s.container.ImageExists(tag) {
		if err := s.EnsureImage(); err != nil {
			return err
		}
		if !s.container.ImageExists(tag) {
			return fmt.Errorf("cannot create base for %s: its image is gone and only the current Containerfile can be built", tag)
		}
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if _, err := os.Stat(base); err == nil {
		return nil // Someone exported it while we waited
	}
	if err := os.MkdirAll(filepath.Dir(base), hiddenDirMode); err != nil {
		return err
	}
	slog.Info("Exporting base rootfs (one time per image tag)", "image", tag)
	started := time.Now()
	if err := s.btrfs.CreateSubvolume(base); err != nil {
		return fmt.Errorf("cannot create base subvolume for %s: %w", tag, err)
	}
	// Export recipe: a throwaway (never started) container, streamed out with
	// permissions and xattrs intact, then sealed read-only so nothing can dirty
	// what every app rootfs shares.
	ctr, err := s.container.CreateFrom(tag, "true")
	if err != nil {
		_ = s.btrfs.DeleteSubvolume(base)
		return fmt.Errorf("cannot create export container for %s: %w", tag, err)
	}
	if err := s.container.ExportRootfs(exportTimeout, ctr, base); err != nil {
		_ = s.container.RemoveForce(ctr)
		_ = s.btrfs.DeleteSubvolume(base) // a half-exported base must not pass the exists check
		return fmt.Errorf("cannot export base rootfs for %s: %w", tag, err)
	}
	_ = s.container.RemoveForce(ctr)
	if err := s.btrfs.SetReadOnly(base, true); err != nil {
		return fmt.Errorf("cannot seal base rootfs for %s: %w", tag, err)
	}
	slog.Info("Base rootfs ready", "image", tag, "took", time.Since(started).Round(time.Second))
	return nil
}

// EnsureRootfs makes sure an app's writable rootfs subvolume exists: a snapshot
// of its pinned tag's base, chowned to the app's id block (crun on this stack
// cannot idmap-mount a --rootfs, so ownership is baked in; ~1.6s once, and data
// extents stay shared with the base).
//
// THE INVARIANT: an app's rootfs, once created, is NEVER recreated or reset by
// hostit. Everything the app installed (apt packages, pip, config under /etc)
// lives in it, and new base tags affect only NEW apps. If the rootfs exists,
// this returns without touching it -- whatever the current image or base is.
func (s *Service) EnsureRootfs(a *store.App, ids IDs) error {
	rootfs := s.RootfsPath(a.ID)
	if _, err := os.Stat(rootfs); err == nil {
		return nil
	}
	// An app from before pinning (empty tag) ran the current image, so that is
	// the base its rootfs comes from -- same fallback as EnsureBase's.
	tag := a.ImageTag
	if tag == "" {
		tag = ImageTag()
	}
	if err := s.EnsureBase(tag); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rootfs), hiddenDirMode); err != nil {
		return err
	}
	if err := s.btrfs.Snapshot(s.BasePath(tag), rootfs, false); err != nil {
		return fmt.Errorf("cannot snapshot rootfs for %s: %w", a.Name, err)
	}
	return s.chownRootfs(rootfs, ids)
}

// ForkRootfs seeds a new app's rootfs from the SOURCE app's rootfs (not the
// base), so a fork carries the source's installed packages -- the same semantics
// as forking the home. Extents shared between source and fork are exclusive to
// neither budget until they diverge; accepted, exactly like home forks.
func (s *Service) ForkRootfs(srcID, dstID string, ids IDs) error {
	dst := s.RootfsPath(dstID)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	src := s.RootfsPath(srcID)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("cannot fork rootfs: source rootfs %s does not exist: %w", src, err)
	}
	if err := s.btrfs.Snapshot(src, dst, false); err != nil {
		return fmt.Errorf("cannot fork rootfs: %w", err)
	}
	return s.chownRootfs(dst, ids)
}

// DeleteRootfs removes an app's rootfs subvolume; used by app delete and by the
// orphan reconcile.
func (s *Service) DeleteRootfs(id string) error {
	return s.btrfs.DeleteSubvolume(s.RootfsPath(id))
}

// RootfsIDs lists the app ids that have a rootfs subvolume, for the orphan
// reconcile. A missing .rootfs dir (fresh host, pre-migration) is just empty.
func (s *Service) RootfsIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.appsDir, rootfsDirName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.Name())
	}
	return ids, nil
}

// PruneOldBases removes base subvolumes that are neither the current tag nor
// pinned by any app. A pinned base may NEVER be deleted: its data extents are
// shared with every app pinned to it, and deleting it would convert them into
// each app's EXCLUSIVE bytes -- silently charging their disk budgets (and likely
// wedging them against their caps).
func (s *Service) PruneOldBases() {
	entries, err := os.ReadDir(filepath.Join(s.appsDir, basesDirName))
	if err != nil {
		return // no bases yet, nothing to prune
	}
	inUse, err := s.store.ImageTagsInUse()
	if err != nil {
		slog.Warn("Cannot read pinned image tags; skipping base prune to be safe", "error", err)
		return
	}
	keep := map[string]bool{baseDirName(ImageTag()): true}
	for tag := range inUse {
		keep[baseDirName(tag)] = true
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		path := filepath.Join(s.appsDir, basesDirName, e.Name())
		if err := s.btrfs.DeleteSubvolume(path); err != nil {
			slog.Warn("Cannot remove old base rootfs", "path", path, "error", err)
			continue
		}
		slog.Info("Removed an old base rootfs", "path", path)
	}
}

// chownRootfs hands the whole rootfs to the app's id block; shelled out because
// chown -R over ~57k files is what the tool is fast at, and it records cleanly
// through the runner in tests.
func (s *Service) chownRootfs(path string, ids IDs) error {
	if _, err := s.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", ids.UID, ids.GID), path); err != nil {
		return fmt.Errorf("cannot chown rootfs %s: %w", path, err)
	}
	return nil
}

// baseDirName is the directory a tag's base lives in: the content hash after the
// tag's colon (unique per tag, safe as a path element).
func baseDirName(tag string) string {
	if i := strings.LastIndex(tag, ":"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}
