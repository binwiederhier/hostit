package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// storageConfRelPath is where each app user's podman storage config lives
	storageConfRelPath = ".config/containers/storage.conf"
	// imageStoreSubDir holds the shared, read-only workspace image below DataDir
	imageStoreSubDir = "imagestore"
	// workspaceSubDir stages the Containerfile the shared image is built from
	workspaceSubDir = "workspace"
)

// storageConf is the podman storage configuration handed to every app user: it
// points at the daemon-managed read-only image store, so nobody has to build or
// store their own copy of the workspace image
func storageConf(imageStoreDir string) string {
	return fmt.Sprintf(`# Managed by hostit. The shared image store holds the workspace image,
# so it is neither rebuilt nor duplicated per app.
[storage]
driver = "overlay"

[storage.options]
additionalimagestores = [%q]
`, imageStoreDir)
}

// EnsureSharedImage builds the workspace image into the shared read-only store
// unless it is already there. Called once at daemon start; app containers then
// reference the image without a per-user build (which took ~40s and 233 MB each).
func (m *Manager) EnsureSharedImage() error {
	if m.ops.SharedImageExists(m.imageStoreDir(), workspaceImage) {
		return nil
	}
	contextDir := filepath.Join(m.config.DataDir, workspaceSubDir)
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Containerfile"), []byte(workspaceContainerfile), 0o644); err != nil {
		return err
	}
	slog.Info("Building shared workspace image (one time, takes a minute)", "image", workspaceImage, "store", m.imageStoreDir())
	if err := m.ops.BuildSharedImage(m.imageStoreDir(), contextDir, workspaceImage); err != nil {
		return fmt.Errorf("cannot build shared workspace image: %w", err)
	}
	slog.Info("Shared workspace image ready", "image", workspaceImage)
	return nil
}

// imageStoreDir is the shared image store path
func (m *Manager) imageStoreDir() string {
	return filepath.Join(m.config.DataDir, imageStoreSubDir)
}
