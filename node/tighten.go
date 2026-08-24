package node

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"heckel.io/hostit/workspace"
)

// TightenAppHomes sets every app's home/app to 0700, the mode a freshly
// provisioned one now gets (workspace.filesDirMode).
//
// It exists for the apps already on disk when the fix lands. The apps directory
// above these homes is world-traversable on purpose -- sshd walks it to reach
// each app user's authorized_keys -- and while the homes were 0755 that same
// traversal let any tenant read every other tenant's files through the raw view
// mounted into every container. New homes are 0700 from creation; this closes
// the ones that predate it, so a single deploy secures the whole box rather than
// only apps created afterwards.
//
// Best effort and idempotent: it returns how many it tightened and logs the
// rest, because one unreadable app must not stop the sweep for the others.
func TightenAppHomes(appsDir string) int {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return 0
	}
	tightened := 0
	for _, e := range entries {
		// Skip the hidden bookkeeping dirs (.bases, .snapshots) -- they are
		// already root-only and hold no app home.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		home := filepath.Join(appsDir, e.Name(), workspace.FilesDir)
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			continue // never provisioned, or mid-teardown
		}
		if err := os.Chmod(home, 0o700); err != nil {
			slog.Warn("Cannot tighten an app home", "path", home, "error", err)
			continue
		}
		tightened++
	}
	return tightened
}
