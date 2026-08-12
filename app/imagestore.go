package app

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
	// workspaceSubDir stages the Containerfile the workspace image is built from
	workspaceSubDir = "workspace"
)

// EnsureWorkspaceImage builds the workspace image unless it already exists.
//
// Containers are created by the root daemon, so there is exactly one image store
// on the host: the image is built once and every app references it, instead of
// each app building and storing its own ~230 MB copy (which is what the rootless
// model forced, since rootless podman keeps its store inside each user's home).
func (m *Manager) EnsureWorkspaceImage() error {
	image := workspaceImageTag()
	if m.container.ImageExists(image) {
		return nil
	}
	// Two apps created at once must not both build this ~230 MB image
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	if m.container.ImageExists(image) {
		return nil // Someone built it while we waited
	}
	contextDir := filepath.Join(m.config.DataDir, workspaceSubDir)
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Containerfile"), []byte(workspaceContainerfile), 0o644); err != nil {
		return err
	}
	slog.Info("Building workspace image (one time, takes a minute)", "image", image)
	started := time.Now()
	if err := m.container.Build(image, contextDir); err != nil {
		return fmt.Errorf("cannot build workspace image: %w", err)
	}
	slog.Info("Workspace image ready", "image", image, "took", time.Since(started).Round(time.Second))
	return nil
}

// PruneOldWorkspaceImages removes workspace images that are no longer the
// current one. Each Containerfile change leaves its predecessor behind, and one
// of these is well over a gigabyte on a small disk. Removal is best effort:
// podman refuses while a container still references an image, and the next
// start tries again once they have moved on.
func (m *Manager) PruneOldWorkspaceImages() {
	out, err := m.container.Images()
	if err != nil {
		slog.Warn("Cannot list images to prune", "error", err)
		return
	}
	// Apps are pinned to the tag they were built with, so an old tag is not garbage
	// just because it is no longer current: it cannot be rebuilt (its Containerfile
	// is gone), so never remove a tag an app is still pinned to.
	inUse, err := m.store.ImageTagsInUse()
	if err != nil {
		slog.Warn("Cannot read pinned image tags; skipping image prune to be safe", "error", err)
		return
	}
	current := workspaceImageTag()
	for _, image := range strings.Split(strings.TrimSpace(out), "\n") {
		image = strings.TrimSpace(image)
		if !strings.HasPrefix(image, workspaceImagePrefix+":") || image == current || inUse[image] {
			continue
		}
		if err := m.container.RemoveImage(image); err != nil {
			slog.Info("Keeping an old workspace image; something still uses it", "image", image)
			continue
		}
		slog.Info("Removed an old workspace image", "image", image)
	}
}

// ensureAppImage makes sure the image an app will actually run is present. That is
// its pinned tag, not necessarily the current one: an app pinned to an image that
// already exists needs nothing built, so recreating it never blocks on a build. A
// missing image is built as the current one (the only Containerfile we have), which
// is the path a new or unpinned app takes.
func (m *Manager) ensureAppImage(a *store.App) error {
	image := a.ImageTag
	if image == "" {
		image = workspaceImageTag()
	}
	if m.container.ImageExists(image) {
		return nil
	}
	return m.EnsureWorkspaceImage()
}

// PinUnpinnedApps backfills any app with no pinned workspace image tag (apps from
// before image pinning) with the image it is CURRENTLY running, so a base-image
// change in this very release leaves them on the image they already run rather than
// recreating them onto a new (unbuilt) one -- which would block startup on a build.
// Falls back to the current tag only when the running image cannot be read.
// Idempotent; a no-op once every app is pinned. Must run before the id-keying
// migration so it recreates apps onto their pinned image.
func (m *Manager) PinUnpinnedApps() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps to pin workspace images", "error", err)
		return
	}
	for _, a := range apps {
		if a.ImageTag != "" {
			continue // already pinned
		}
		image := m.runningImage(a)
		if image == "" {
			image = workspaceImageTag()
		}
		if err := m.store.SetAppImageTag(a.Name, image); err != nil {
			slog.Warn("Cannot pin app to its workspace image", "app", a.Name, "error", err)
		}
	}
}

// runningImage reports the workspace image an app's container is running, or "" if
// it cannot be read. The container may still be name-keyed (this runs before the
// id-keying migration) or already id-keyed, so both names are tried.
func (m *Manager) runningImage(a *store.App) string {
	for _, name := range []string{containerNameForID(a.ID), containerPrefix + a.Name} {
		out, err := m.container.ImageOf(name)
		if err != nil {
			continue
		}
		image := strings.TrimSpace(out)
		if strings.HasPrefix(image, workspaceImagePrefix+":") {
			return image
		}
	}
	return ""
}
