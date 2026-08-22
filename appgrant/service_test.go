package appgrant

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignAndVerify(t *testing.T) {
	t.Parallel()
	signer := NewSigner("session-key", time.Hour)
	verifier, err := NewVerifier(signer.PublicKey())
	require.NoError(t, err)

	value, err := signer.Sign("blog", "u1")
	require.NoError(t, err)
	app, userID, err := verifier.Verify(value)
	require.NoError(t, err)
	assert.Equal(t, "blog", app)
	assert.Equal(t, "u1", userID)
}

// The whole reason this is Ed25519 rather than an HMAC: the proxy needs to
// check grants while control is down, and it must not be able to MINT one.
// Holding the public key has to be useless for issuing access.
func TestTheVerifierCannotMint(t *testing.T) {
	t.Parallel()
	signer := NewSigner("session-key", time.Hour)
	verifier, err := NewVerifier(signer.PublicKey())
	require.NoError(t, err)

	// Everything the verifier holds is public, so the only way to produce a
	// value it accepts is to have the private half.
	forged := "secret|attacker|" + "9999999999" + "|" + signer.PublicKey()
	_, _, err = verifier.Verify(forged)
	assert.ErrorIs(t, err, ErrInvalidGrant)

	other := NewSigner("a different session key", time.Hour)
	value, err := other.Sign("secret", "attacker")
	require.NoError(t, err)
	_, _, err = verifier.Verify(value)
	assert.ErrorIs(t, err, ErrInvalidGrant, "a grant from another instance's key is not ours")
}

// Both halves are derived from the session key, so every control that shares
// that key agrees on grants without any extra material to distribute.
func TestTheKeypairIsDerivedFromTheSessionKey(t *testing.T) {
	t.Parallel()
	a, b := NewSigner("same-key", time.Hour), NewSigner("same-key", time.Hour)
	assert.Equal(t, a.PublicKey(), b.PublicKey())

	value, err := a.Sign("blog", "u1")
	require.NoError(t, err)
	verifier, err := NewVerifier(b.PublicKey())
	require.NoError(t, err)
	_, _, err = verifier.Verify(value)
	assert.NoError(t, err, "the other half verifies it")

	different := NewSigner("another-key", time.Hour)
	assert.NotEqual(t, a.PublicKey(), different.PublicKey())
}

func TestVerifyRejectsTamperingAndExpiry(t *testing.T) {
	t.Parallel()
	signer := NewSigner("session-key", time.Hour)
	verifier, err := NewVerifier(signer.PublicKey())
	require.NoError(t, err)
	value, err := signer.Sign("blog", "u1")
	require.NoError(t, err)

	// Re-pointing a valid grant at another app is the attack that matters.
	_, _, err = verifier.Verify("secret" + value[len("blog"):])
	assert.ErrorIs(t, err, ErrInvalidGrant)
	_, _, err = verifier.Verify(value + "x")
	assert.ErrorIs(t, err, ErrInvalidGrant)
	_, _, err = verifier.Verify("garbage")
	assert.ErrorIs(t, err, ErrInvalidGrant)
	_, _, err = verifier.Verify("")
	assert.ErrorIs(t, err, ErrInvalidGrant)

	expired, err := NewSigner("session-key", -time.Minute).Sign("blog", "u1")
	require.NoError(t, err)
	_, _, err = verifier.Verify(expired)
	assert.ErrorIs(t, err, ErrInvalidGrant)
}

func TestSignRefusesSeparatorsInItsFields(t *testing.T) {
	t.Parallel()
	signer := NewSigner("session-key", time.Hour)
	_, err := signer.Sign("bl|og", "u1")
	assert.Error(t, err)
	_, err = signer.Sign("blog", "u|1")
	assert.Error(t, err)
}

func TestNewVerifierRejectsRubbish(t *testing.T) {
	t.Parallel()
	_, err := NewVerifier("not base64 at all !!")
	assert.Error(t, err)
	_, err = NewVerifier("")
	assert.Error(t, err)
}
