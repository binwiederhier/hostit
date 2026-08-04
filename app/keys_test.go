package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL a@host"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL b@host"
	keyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL c@host"
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
