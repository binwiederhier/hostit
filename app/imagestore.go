package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	if m.ops.ImageExists(workspaceImage) {
		return nil
	}
	contextDir := filepath.Join(m.config.DataDir, workspaceSubDir)
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Containerfile"), []byte(workspaceContainerfile), 0o644); err != nil {
		return err
	}
	slog.Info("Building workspace image (one time, takes a minute)", "image", workspaceImage)
	if err := m.ops.BuildImage(contextDir, workspaceImage); err != nil {
		return fmt.Errorf("cannot build workspace image: %w", err)
	}
	slog.Info("Workspace image ready", "image", workspaceImage)
	return nil
}
