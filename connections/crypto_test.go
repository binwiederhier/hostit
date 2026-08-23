package connections

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The credential is the whole point of this feature, so it does not sit in the
// database in the clear. A stolen hostit.db must not be a stolen mailbox.
func TestSealedCredentialsRoundTripAndDoNotLeak(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	require.NoError(t, err)

	sealed, err := Seal(key, "refresh-token-abc", nil)
	require.NoError(t, err)
	assert.NotContains(t, sealed, "refresh-token-abc", "the stored form must not contain the secret")

	out, err := Open(key, sealed, nil)
	require.NoError(t, err)
	assert.Equal(t, "refresh-token-abc", out)
}

// Sealing twice gives different ciphertext (a fresh nonce each time), so two
// users with the same password do not have matching rows.
func TestSealIsNotDeterministic(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	require.NoError(t, err)
	a, err := Seal(key, "same", nil)
	require.NoError(t, err)
	b, err := Seal(key, "same", nil)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

// The wrong key fails loudly rather than returning rubbish an app would then
// send to Google as a token.
func TestOpenWithTheWrongKeyFails(t *testing.T) {
	t.Parallel()
	k1, err := NewKey()
	require.NoError(t, err)
	k2, err := NewKey()
	require.NoError(t, err)
	sealed, err := Seal(k1, "secret", nil)
	require.NoError(t, err)

	_, err = Open(k2, sealed, nil)
	assert.Error(t, err)
}

// A sealed credential is bound to the row it belongs to. Without this,
// ciphertext is portable: anyone who can WRITE the database (a bad migration, a
// restore that mixes rows, a future SQL bug) could move one person's sealed
// secret into another's connection and have it decrypt cleanly. GCM
// authenticates the bytes; the binding is what says whose they are.
func TestSealedCredentialsAreBoundToTheirRow(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	require.NoError(t, err)

	mine := Binding("u_victim", "cn_1")
	theirs := Binding("u_attacker", "cn_2")

	sealed, err := Seal(key, "VICTIM-SECRET", mine)
	require.NoError(t, err)

	got, err := Open(key, sealed, mine)
	require.NoError(t, err)
	assert.Equal(t, "VICTIM-SECRET", got)

	_, err = Open(key, sealed, theirs)
	require.Error(t, err, "the same ciphertext must not open in another row")

	// A different key still fails, binding or not
	other, _ := NewKey()
	_, err = Open(other, sealed, mine)
	require.Error(t, err)
}

// Credentials sealed before binding existed must keep working: this runs on a
// live instance with real connections, and breaking them all would mean
// re-authorising every account to ship a hardening change.
func TestCredentialsSealedBeforeBindingStillOpen(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	require.NoError(t, err)

	legacy, err := Seal(key, "OLD-SECRET", nil)
	require.NoError(t, err)

	got, bound, err := OpenLegacyTolerant(key, legacy, Binding("u1", "cn_1"))
	require.NoError(t, err)
	assert.Equal(t, "OLD-SECRET", got)
	assert.False(t, bound, "and it reports that this one is not bound yet, so it can be re-sealed")

	fresh, err := Seal(key, "NEW-SECRET", Binding("u1", "cn_1"))
	require.NoError(t, err)
	got, bound, err = OpenLegacyTolerant(key, fresh, Binding("u1", "cn_1"))
	require.NoError(t, err)
	assert.Equal(t, "NEW-SECRET", got)
	assert.True(t, bound)

	// Tolerance must not extend to opening a BOUND credential in the wrong row
	_, _, err = OpenLegacyTolerant(key, fresh, Binding("u2", "cn_2"))
	assert.Error(t, err)
}
