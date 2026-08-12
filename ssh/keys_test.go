package ssh

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	// Three distinct keys: the merge logic compares key material, not comments
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBERERERERERERERERERERERERERERERERERERERERER a@host"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIi b@host"
	keyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMz c@host"
)

func TestMergeAuthorizedKeysIntoEmptyFile(t *testing.T) {
	t.Parallel()
	out := MergeAuthorizedKeys("", []string{keyA, keyB})
	assert.Contains(t, out, keyA)
	assert.Contains(t, out, keyB)
	assert.Contains(t, out, managedBeginMarker)
	assert.Contains(t, out, managedEndMarker)
}

func TestMergeAuthorizedKeysPreservesForeignKeys(t *testing.T) {
	t.Parallel()
	// A key someone added by hand, outside hostit
	existing := "# my deploy key\n" + keyC + "\n"
	out := MergeAuthorizedKeys(existing, []string{keyA})
	assert.Contains(t, out, keyC, "hand-added keys must survive")
	assert.Contains(t, out, "# my deploy key", "their comments too")
	assert.Contains(t, out, keyA)
}

func TestMergeAuthorizedKeysReplacesOnlyTheManagedBlock(t *testing.T) {
	t.Parallel()
	first := MergeAuthorizedKeys("", []string{keyA, keyB})
	withForeign := first + "\n# added later by hand\n" + keyC + "\n"
	// hostit now manages a different set
	second := MergeAuthorizedKeys(withForeign, []string{keyA})
	assert.Contains(t, second, keyA, "still-managed keys stay")
	assert.NotContains(t, second, keyB, "removed managed keys go away")
	assert.Contains(t, second, keyC, "foreign keys are untouched")
	assert.Equal(t, 1, strings.Count(second, managedBeginMarker), "exactly one managed block")
	assert.Equal(t, 1, strings.Count(second, managedEndMarker))
}

func TestMergeAuthorizedKeysWithNoManagedKeys(t *testing.T) {
	t.Parallel()
	existing := MergeAuthorizedKeys(keyC+"\n", []string{keyA})
	out := MergeAuthorizedKeys(existing, nil)
	assert.NotContains(t, out, keyA, "managed keys are cleared")
	assert.Contains(t, out, keyC, "foreign keys remain")
}

func TestMergeAuthorizedKeysIsIdempotent(t *testing.T) {
	t.Parallel()
	once := MergeAuthorizedKeys("", []string{keyA})
	twice := MergeAuthorizedKeys(once, []string{keyA})
	assert.Equal(t, once, twice)
}

func TestMergeAuthorizedKeysDropsDuplicatesOfManagedKeys(t *testing.T) {
	t.Parallel()
	// Apps written before the managed block existed have hostit's keys as plain
	// lines; they must not end up listed twice
	legacy := keyA + "\n"
	out := MergeAuthorizedKeys(legacy, []string{keyA})
	assert.Equal(t, 1, strings.Count(out, keyA), "a managed key appears exactly once")
	// The same applies when a user pastes a key hostit already manages
	out = MergeAuthorizedKeys(keyC+"\n"+keyA+"\n", []string{keyA, keyB})
	assert.Equal(t, 1, strings.Count(out, keyA))
	assert.Contains(t, out, keyC, "genuinely foreign keys are still kept")
	assert.Contains(t, out, keyB)
}
