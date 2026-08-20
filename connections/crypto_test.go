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

	sealed, err := Seal(key, "refresh-token-abc")
	require.NoError(t, err)
	assert.NotContains(t, sealed, "refresh-token-abc", "the stored form must not contain the secret")

	out, err := Open(key, sealed)
	require.NoError(t, err)
	assert.Equal(t, "refresh-token-abc", out)
}

// Sealing twice gives different ciphertext (a fresh nonce each time), so two
// users with the same password do not have matching rows.
func TestSealIsNotDeterministic(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	require.NoError(t, err)
	a, err := Seal(key, "same")
	require.NoError(t, err)
	b, err := Seal(key, "same")
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
	sealed, err := Seal(k1, "secret")
	require.NoError(t, err)

	_, err = Open(k2, sealed)
	assert.Error(t, err)
}
