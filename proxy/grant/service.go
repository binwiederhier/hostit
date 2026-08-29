// Package appgrant signs and verifies the per-app credential a visitor carries
// on a private app's own hostname.
//
// It is Ed25519 rather than an HMAC for one reason: the PROXY has to check
// grants, and the proxy must not be able to issue them. A shared secret would
// give whoever holds it the power to mint access to every private app; a
// public key is useless for anything but saying no. That matters because the
// proxy keeps serving private apps while control is down, which is the whole
// point of it holding the routing table in the first place.
//
// The keypair is derived from the session key, so every control that shares
// that key agrees on grants with no extra material to distribute or rotate.
package grant

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// keyLabel separates the grant keypair from the session key it is derived
	// from, so a session cookie can never be replayed as a grant.
	keyLabel = "hostit-app-grant-ed25519"
	// fields is how many "|"-separated parts a grant has: app, user, expiry, sig.
	fields = 4
)

var (
	// ErrInvalidGrant covers every way a grant can fail to convince us: bad
	// shape, bad signature, wrong key, or expired. They are deliberately one
	// error -- the difference is never something the visitor should learn.
	ErrInvalidGrant = errors.New("invalid or expired app grant")
)

// Signer issues grants. Only control holds one.
type Signer struct {
	key ed25519.PrivateKey
	ttl time.Duration
}

// Verifier checks grants. Control has one too, but the proxy has ONLY this.
type Verifier struct {
	key ed25519.PublicKey
}

// NewSigner derives the grant keypair from the session key.
func NewSigner(sessionKey string, ttl time.Duration) *Signer {
	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write([]byte(keyLabel))
	return &Signer{key: ed25519.NewKeyFromSeed(mac.Sum(nil)), ttl: ttl}
}

// NewVerifier builds a verifier from a public key as PublicKey returns it.
func NewVerifier(publicKey string) (*Verifier, error) {
	raw, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil {
		return nil, fmt.Errorf("cannot decode the grant public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("grant public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return &Verifier{key: ed25519.PublicKey(raw)}, nil
}

// PublicKey is the verifying half, safe to hand to anything that only needs to
// check grants -- and safe to write to disk, which is what lets the proxy keep
// checking them across a restart while control is unreachable.
func (s *Signer) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Verifier returns the checking half of this signer, for control's own use.
func (s *Signer) Verifier() *Verifier {
	return &Verifier{key: s.key.Public().(ed25519.PublicKey)}
}

// Sign issues a grant naming one app and one user: "<app>|<user>|<expiry>|<sig>".
func (s *Signer) Sign(app, userID string) (string, error) {
	if strings.Contains(app, "|") || strings.Contains(userID, "|") {
		return "", fmt.Errorf("invalid app %q or user id %q", app, userID)
	}
	payload := fmt.Sprintf("%s|%s|%d", app, userID, time.Now().Add(s.ttl).Unix())
	return payload + "|" + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.key, []byte(payload))), nil
}

// Verify checks a grant and returns the app and user it names. It says nothing
// about whether that user may still see that app -- that is a live question,
// answered against the access sets, not against the credential.
func (v *Verifier) Verify(value string) (string, string, error) {
	parts := strings.Split(value, "|")
	if len(parts) != fields {
		return "", "", ErrInvalidGrant
	}
	payload := strings.Join(parts[:3], "|")
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !ed25519.Verify(v.key, []byte(payload), sig) {
		return "", "", ErrInvalidGrant
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().After(time.Unix(expiry, 0)) {
		return "", "", ErrInvalidGrant
	}
	return parts[0], parts[1], nil
}
