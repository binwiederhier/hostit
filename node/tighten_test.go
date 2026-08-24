package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every app's home must be 0700: the apps dir above it is world-traversable so
// sshd can reach each user's keys, and at 0755 a home was readable by any other
// tenant through the raw view. New homes get 0700 from filesDirMode; this sweep
// tightens the ones that predate the fix, so a deploy closes the hole for the
// apps already on disk rather than only for the next one created.
func TestTightenAppHomesLocksExistingHomes(t *testing.T) {
	appsDir := t.TempDir()
	// Two apps with world-readable homes, and the hidden dirs that must be left
	// alone.
	for _, id := range []string{"aaaa", "bbbb"} {
		home := filepath.Join(appsDir, id, "home", "app")
		require.NoError(t, os.MkdirAll(home, 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, ".bases", "x"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, ".snapshots", "y"), 0o700))

	n := TightenAppHomes(appsDir)
	assert.Equal(t, 2, n, "both real app homes tightened")

	for _, id := range []string{"aaaa", "bbbb"} {
		info, err := os.Stat(filepath.Join(appsDir, id, "home", "app"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), id)
	}
}

// An app that has no home/app yet (never provisioned, or mid-teardown) is
// skipped rather than failing the sweep.
func TestTightenAppHomesSkipsWhatItCannotFind(t *testing.T) {
	appsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(appsDir, "cccc"), 0o755)) // no home/app
	assert.Equal(t, 0, TightenAppHomes(appsDir))
	assert.Equal(t, 0, TightenAppHomes(filepath.Join(appsDir, "does-not-exist")))
}
