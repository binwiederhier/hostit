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
	// basesDirName holds the read-only base rootfs subvolumes, one per image tag:
	// a hidden dir beside the app subvolumes under the apps pool (like
	// .snapshots), so everything tenant-writable shares one qgroup-capable
	// filesystem.
	basesDirName = ".bases"
	// hiddenDirMode keeps .bases root-only; apps reach their subvolume through
	// the container runtime, never by path.
	hiddenDirMode = 0o700
	// filesDirMode is home/app inside a fresh app subvolume: the app user and
	// hostit only, matching what useradd gives a home.
	filesDirMode = 0o750
	// exportTimeout bounds a base export (podman export | tar). Measured at ~40s
	// for the current ~860 MB image; generous headroom for a loaded small host.
	exportTimeout = 15 * time.Minute
	// chownTimeout bounds a recursive chown of an app tree. The inode count under
	// it is tenant-controlled (many empty files fit in a small quota), so the walk
	// must not be allowed to hold a request forever; measured at ~1.6s for the
	// base's ~57k files, so the bound only cuts off pathological trees.
	chownTimeout = 15 * time.Minute
)

// BasePath is the base subvolume for an image tag. The directory is named by the
// tag's content hash (the part after the colon); the full tag has "/" and ":" in
// it and cannot be a path element.
func (s *Service) BasePath(tag string) string {
	return filepath.Join(s.appsDir, basesDirName, baseDirName(tag))
}

// AppSubvolumePath is an app's one writable subvolume: the full OS tree its
// container runs (--rootfs) with the app's files at FilesDir inside it. Keyed
// on the app's stable id, so a rename never moves it.
func (s *Service) AppSubvolumePath(id string) string {
	return filepath.Join(s.appsDir, id)
}

// EnsureBase makes sure the read-only base subvolume for an image tag exists,
// building the image and exporting its filesystem if needed. The export is the
// one-time cost per tag (~40s); every app rootfs is then an instant snapshot of
// it. Serialized with the image-build mutex: two exports at once would thrash a
// small host the same way two builds would.
func (s *Service) EnsureBase(tag string) error {
	if tag == "" {
		tag = ImageTag() // pre-pinning fallback; historical (PinImageTags backfills every row)
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
	// The whole export runs against a temp subvolume; only the final rename
	// publishes it at the base path. The exists check above is the only guard
	// against reusing a base, so a daemon killed mid-export (a deploy restart hit
	// exactly this window on stage) must never leave a partial tree at the base
	// path: every app rootfs snapshotted from it would be missing files -- the
	// stage incident lost the ELF interpreter and bricked every container.
	tmp := base + ".tmp"
	if _, err := os.Stat(tmp); err == nil {
		_ = s.btrfs.DeleteSubvolume(tmp) // leftover of a killed export; discard
	}
	slog.Info("Exporting base rootfs (one time per image tag)", "image", tag)
	started := time.Now()
	if err := s.btrfs.CreateSubvolume(tmp); err != nil {
		return fmt.Errorf("cannot create base subvolume for %s: %w", tag, err)
	}
	// Export recipe: a throwaway (never started) container, streamed out with
	// permissions and xattrs intact, then sealed read-only so nothing can dirty
	// what every app rootfs shares.
	ctr, err := s.container.CreateFrom(tag, "true")
	if err != nil {
		_ = s.btrfs.DeleteSubvolume(tmp)
		return fmt.Errorf("cannot create export container for %s: %w", tag, err)
	}
	if err := s.container.ExportRootfs(exportTimeout, ctr, tmp); err != nil {
		_ = s.container.RemoveForce(ctr)
		_ = s.btrfs.DeleteSubvolume(tmp)
		return fmt.Errorf("cannot export base rootfs for %s: %w", tag, err)
	}
	_ = s.container.RemoveForce(ctr)
	if err := s.btrfs.SetReadOnly(tmp, true); err != nil {
		_ = s.btrfs.DeleteSubvolume(tmp)
		return fmt.Errorf("cannot seal base rootfs for %s: %w", tag, err)
	}
	if err := s.btrfs.MoveSubvolume(tmp, base); err != nil {
		_ = s.btrfs.DeleteSubvolume(tmp)
		return fmt.Errorf("cannot publish base rootfs for %s: %w", tag, err)
	}
	slog.Info("Base rootfs ready", "image", tag, "took", time.Since(started).Round(time.Second))
	return nil
}

// EnsureAppSubvolume makes sure an app's subvolume exists: a snapshot of its
// pinned tag's base with the files directory (FilesDir) created inside, chowned
// to the app's id block (crun on this stack cannot idmap-mount a --rootfs, so
// ownership is baked in; ~1.6s once, and data extents stay shared with the base).
//
// THE INVARIANT: an app's subvolume, once created, is NEVER recreated or reset
// by hostit. The app's files AND everything it installed (apt packages, pip,
// config under /etc) live in it, and new base tags affect only NEW apps. If the
// subvolume exists, this returns without touching it -- whatever the current
// image or base is.
func (s *Service) EnsureAppSubvolume(a *store.App, ids IDs) error {
	subvol := s.AppSubvolumePath(a.ID)
	if _, err := os.Stat(subvol); err == nil {
		return nil
	}
	// An app from before pinning (empty tag) ran the current image, so that is
	// the base its subvolume comes from -- same historical fallback as
	// EnsureBase's; PinImageTags backfills every row before any app migrates.
	tag := a.ImageTag
	if tag == "" {
		tag = ImageTag()
	}
	if err := s.EnsureBase(tag); err != nil {
		return err
	}
	if err := s.btrfs.Snapshot(s.BasePath(tag), subvol, false); err != nil {
		return fmt.Errorf("cannot snapshot app subvolume for %s: %w", a.Name, err)
	}
	// The base may not ship a /home/app; create the files dir before the chown
	// below so it belongs to the app's block like everything else.
	if err := os.MkdirAll(filepath.Join(subvol, FilesDir), filesDirMode); err != nil {
		return err
	}
	return s.ChownTree(subvol, ids)
}

// ForkAppSubvolume seeds a new app's subvolume from a seed subvolume: the
// SOURCE app's subvolume (or a whole-app snapshot of it), so a fork carries the
// source's files and installed packages in one CoW copy. Extents shared between
// source and fork are exclusive to neither budget until they diverge; accepted.
func (s *Service) ForkAppSubvolume(src, dstID string, ids IDs) error {
	dst := s.AppSubvolumePath(dstID)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("cannot fork app subvolume: seed %s does not exist: %w", src, err)
	}
	if err := s.btrfs.Snapshot(src, dst, false); err != nil {
		return fmt.Errorf("cannot fork app subvolume: %w", err)
	}
	return s.ChownTree(dst, ids)
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

// ChownTree hands a whole tree to an app's id block; shelled out because chown
// -R over ~57k files is what the tool is fast at, and it records cleanly
// through the runner in tests. Exported as THE recursive-chown for app trees
// (fresh subvolumes, forks, rollbacks, migration staging), so the time bound
// and the flag shape live in exactly one place.
func (s *Service) ChownTree(path string, ids IDs) error {
	if _, err := s.runner.RunTimeout(chownTimeout, "chown", "-R", fmt.Sprintf("%d:%d", ids.UID, ids.GID), path); err != nil {
		return fmt.Errorf("cannot chown app tree %s: %w", path, err)
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
