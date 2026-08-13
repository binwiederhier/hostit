package workspace

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/container"
	"heckel.io/hostit/store"
)

// containerfile builds the default workspace image: small, but with
// everything needed for ssh/scp/sftp/rsync sessions and quick demo apps. The
// hostit binary itself is bind-mounted, not baked in. Embedded (not a raw string)
// so its bytes -- which the image tag hashes -- stay stable and easy to edit.
//
//go:embed workspace.Containerfile
var containerfile string

const (
	// imagePrefix names the default image for static/run mode apps, built
	// once into the daemon's (root) image store and shared by every app. The tag
	// after it is a hash of the Containerfile, so editing that file is enough to
	// get the new image built and the containers recreated onto it.
	imagePrefix = "localhost/hostit-workspace"
	// workspaceSubDir stages the Containerfile the workspace image is built from
	workspaceSubDir = "workspace"
)

// Service owns the workspace image lifecycle in the host's shared (root) image
// store: building the current image, keeping an app's pinned image present, and
// pruning tags nothing references anymore.
type Service struct {
	container container.Interface
	store     *store.Store
	dataDir   string

	buildMu sync.Mutex // Serializes image builds; two at once OOM a small host
}

// New builds a workspace image Service from the container runtime, the store
// (for the pinned tags apps hold) and the daemon's data directory (where the
// build context is staged).
func New(ct container.Interface, st *store.Store, dataDir string) *Service {
	return &Service{container: ct, store: st, dataDir: dataDir}
}

// ImageTag is the image built from the current Containerfile
func ImageTag() string {
	return imageTagFor(containerfile)
}

// imageTagFor derives a tag from what the image is built out of
func imageTagFor(containerfile string) string {
	sum := sha256.Sum256([]byte(containerfile))
	return imagePrefix + ":" + hex.EncodeToString(sum[:6])
}

// EnsureImage builds the workspace image unless it already exists.
//
// Containers are created by the root daemon, so there is exactly one image store
// on the host: the image is built once and every app references it, instead of
// each app building and storing its own ~230 MB copy (which is what the rootless
// model forced, since rootless podman keeps its store inside each user's home).
func (s *Service) EnsureImage() error {
	image := ImageTag()
	if s.container.ImageExists(image) {
		return nil
	}
	// Two apps created at once must not both build this ~230 MB image
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.container.ImageExists(image) {
		return nil // Someone built it while we waited
	}
	contextDir := filepath.Join(s.dataDir, workspaceSubDir)
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		return err
	}
	slog.Info("Building workspace image (one time, takes a minute)", "image", image)
	started := time.Now()
	if err := s.container.Build(image, contextDir); err != nil {
		return fmt.Errorf("cannot build workspace image: %w", err)
	}
	slog.Info("Workspace image ready", "image", image, "took", time.Since(started).Round(time.Second))
	return nil
}

// PruneOldImages removes workspace images that are no longer the
// current one. Each Containerfile change leaves its predecessor behind, and one
// of these is well over a gigabyte on a small disk. Removal is best effort:
// podman refuses while a container still references an image, and the next
// start tries again once they have moved on.
func (s *Service) PruneOldImages() {
	out, err := s.container.Images()
	if err != nil {
		slog.Warn("Cannot list images to prune", "error", err)
		return
	}
	// Apps are pinned to the tag they were built with, so an old tag is not garbage
	// just because it is no longer current: it cannot be rebuilt (its Containerfile
	// is gone), so never remove a tag an app is still pinned to.
	inUse, err := s.store.ImageTagsInUse()
	if err != nil {
		slog.Warn("Cannot read pinned image tags; skipping image prune to be safe", "error", err)
		return
	}
	current := ImageTag()
	for _, image := range strings.Split(strings.TrimSpace(out), "\n") {
		image = strings.TrimSpace(image)
		if !strings.HasPrefix(image, imagePrefix+":") || image == current || inUse[image] {
			continue
		}
		if err := s.container.RemoveImage(image); err != nil {
			slog.Info("Keeping an old workspace image; something still uses it", "image", image)
			continue
		}
		slog.Info("Removed an old workspace image", "image", image)
	}
}

// EnsureAppImage makes sure the image an app will actually run is present. That is
// its pinned tag, not necessarily the current one: an app pinned to an image that
// already exists needs nothing built, so recreating it never blocks on a build. A
// missing image is built as the current one (the only Containerfile we have), which
// is the path a new or unpinned app takes.
func (s *Service) EnsureAppImage(a *store.App) error {
	image := a.ImageTag
	if image == "" {
		image = ImageTag()
	}
	if s.container.ImageExists(image) {
		return nil
	}
	return s.EnsureImage()
}
