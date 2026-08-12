package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateKeys(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKeys([]string{keyA, keyB}))
	require.Error(t, ValidateKeys([]string{"not a key"}))
	require.Error(t, ValidateKeys([]string{keyA, "ssh-ed25519 garbage"}))
}

func TestWriteAuthorizedKeysRefusesASymlinkedSSHDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	outside := t.TempDir()
	// The app user owns their home, so they can replace .ssh with a link to
	// somewhere that matters. Root must neither write into it nor give it away.
	require.NoError(t, os.Symlink(outside, filepath.Join(home, ".ssh")))

	root, err := os.OpenRoot(home)
	require.NoError(t, err)
	defer root.Close()
	err = writeAuthorizedKeysIn(root, []string{keyA}, os.Getuid(), os.Getgid())
	require.Error(t, err, "a symlinked .ssh must be refused")

	_, err = os.Stat(filepath.Join(outside, "authorized_keys"))
	assert.True(t, os.IsNotExist(err), "nothing may be written outside the home")
}

func TestWriteAuthorizedKeysWritesARealSSHDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root, err := os.OpenRoot(home)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, writeAuthorizedKeysIn(root, []string{keyA}, os.Getuid(), os.Getgid()))
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	require.NoError(t, err)
	assert.Contains(t, string(b), keyA)
	stat, err := os.Stat(filepath.Join(home, ".ssh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), stat.Mode().Perm())
}
