package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Three distinct keys: the merge logic compares key material, not comments
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBERERERERERERERERERERERERERERERERERERERERER a@host"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIi b@host"
	keyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMz c@host"
)

func TestMergeAuthorizedKeysIntoEmptyFile(t *testing.T) {
	t.Parallel()
	out := mergeAuthorizedKeys("", []string{keyA, keyB})
	assert.Contains(t, out, keyA)
	assert.Contains(t, out, keyB)
	assert.Contains(t, out, managedBeginMarker)
	assert.Contains(t, out, managedEndMarker)
}

func TestMergeAuthorizedKeysPreservesForeignKeys(t *testing.T) {
	t.Parallel()
	// A key someone added by hand, outside hostit
	existing := "# my deploy key\n" + keyC + "\n"
	out := mergeAuthorizedKeys(existing, []string{keyA})
	assert.Contains(t, out, keyC, "hand-added keys must survive")
	assert.Contains(t, out, "# my deploy key", "their comments too")
	assert.Contains(t, out, keyA)
}

func TestMergeAuthorizedKeysReplacesOnlyTheManagedBlock(t *testing.T) {
	t.Parallel()
	first := mergeAuthorizedKeys("", []string{keyA, keyB})
	withForeign := first + "\n# added later by hand\n" + keyC + "\n"
	// hostit now manages a different set
	second := mergeAuthorizedKeys(withForeign, []string{keyA})
	assert.Contains(t, second, keyA, "still-managed keys stay")
	assert.NotContains(t, second, keyB, "removed managed keys go away")
	assert.Contains(t, second, keyC, "foreign keys are untouched")
	assert.Equal(t, 1, strings.Count(second, managedBeginMarker), "exactly one managed block")
	assert.Equal(t, 1, strings.Count(second, managedEndMarker))
}

func TestMergeAuthorizedKeysWithNoManagedKeys(t *testing.T) {
	t.Parallel()
	existing := mergeAuthorizedKeys(keyC+"\n", []string{keyA})
	out := mergeAuthorizedKeys(existing, nil)
	assert.NotContains(t, out, keyA, "managed keys are cleared")
	assert.Contains(t, out, keyC, "foreign keys remain")
}

func TestMergeAuthorizedKeysIsIdempotent(t *testing.T) {
	t.Parallel()
	once := mergeAuthorizedKeys("", []string{keyA})
	twice := mergeAuthorizedKeys(once, []string{keyA})
	assert.Equal(t, once, twice)
}

func TestMergeAuthorizedKeysDropsDuplicatesOfManagedKeys(t *testing.T) {
	t.Parallel()
	// Apps written before the managed block existed have hostit's keys as plain
	// lines; they must not end up listed twice
	legacy := keyA + "\n"
	out := mergeAuthorizedKeys(legacy, []string{keyA})
	assert.Equal(t, 1, strings.Count(out, keyA), "a managed key appears exactly once")
	// The same applies when a user pastes a key hostit already manages
	out = mergeAuthorizedKeys(keyC+"\n"+keyA+"\n", []string{keyA, keyB})
	assert.Equal(t, 1, strings.Count(out, keyA))
	assert.Contains(t, out, keyC, "genuinely foreign keys are still kept")
	assert.Contains(t, out, keyB)
}

func TestSyncKeysRewritesEveryAppOfTheOwner(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "one")
	createTestApp(t, m, "two")
	// A profile key added later must reach every app the user owns
	require.NoError(t, m.SyncKeys("one", []string{keyB}))
	require.NoError(t, m.SyncKeys("two", []string{keyB}))
	for _, name := range []string{"one", "two"} {
		require.Contains(t, ops.authorizedKeys[name], keyB, "app %s must get the profile key", name)
		assert.Contains(t, ops.authorizedKeys[name], testPublicKey, "its own app key stays")
	}
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
	err = writeAuthorizedKeysIn(root, []string{testPublicKey}, os.Getuid(), os.Getgid())
	require.Error(t, err, "a symlinked .ssh must be refused")

	_, err = os.Stat(filepath.Join(outside, "authorized_keys"))
	assert.True(t, os.IsNotExist(err), "nothing may be written outside the home")
	stat, err := os.Stat(outside)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700).Perm()&stat.Mode().Perm(), stat.Mode().Perm()&0o700,
		"the target directory must be untouched")
}

func TestWriteAuthorizedKeysWritesARealSSHDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root, err := os.OpenRoot(home)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, writeAuthorizedKeysIn(root, []string{testPublicKey}, os.Getuid(), os.Getgid()))
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	require.NoError(t, err)
	assert.Contains(t, string(b), testPublicKey)
	stat, err := os.Stat(filepath.Join(home, ".ssh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), stat.Mode().Perm())
}

func TestCreateUserArgsBringNoSkeleton(t *testing.T) {
	t.Parallel()
	// useradd copies /etc/skel into every new home: .bashrc, .profile,
	// .bash_logout, .cloud-locale-test.skip. None of that is the app's, and an
	// app directory should hold the app's files and hostit's own, nothing else.
	args := createUserArgs("blog", "/srv/hostit/apps/blog")
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--no-create-home")
	assert.NotContains(t, joined, "--create-home")
	assert.Contains(t, joined, "--home-dir /srv/hostit/apps/blog")
	assert.Contains(t, joined, "--shell "+userShellFile)
	assert.Contains(t, joined, "--groups "+AppsGroup)
}
