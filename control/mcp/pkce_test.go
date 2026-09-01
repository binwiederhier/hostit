package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PKCE is required for every OAuth 2.1 client, which is what an MCP server's
// authorization server is. hostit's Google login predates it and does without
// (a confidential client whose code never leaves the back channel); an MCP
// server is a stranger, so this is the first place it is genuinely needed.
func TestPKCEChallengeIsTheHashOfTheVerifier(t *testing.T) {
	t.Parallel()
	v, err := NewPKCE()
	require.NoError(t, err)

	// RFC 7636: 43-128 characters from an unreserved alphabet
	assert.GreaterOrEqual(t, len(v.Verifier), 43)
	assert.LessOrEqual(t, len(v.Verifier), 128)
	assert.NotContains(t, v.Verifier, "=", "base64url, unpadded")
	assert.NotContains(t, v.Verifier, "+")
	assert.NotContains(t, v.Verifier, "/")

	sum := sha256.Sum256([]byte(v.Verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), v.Challenge,
		"the challenge is S256 of the verifier, or the token exchange is refused")
	assert.Equal(t, "S256", v.Method, "never plain: plain sends the verifier through the browser")
}

func TestPKCEVerifiersAreNotReused(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := NewPKCE()
		require.NoError(t, err)
		assert.False(t, seen[v.Verifier], "a repeated verifier would let one code redeem another's")
		seen[v.Verifier] = true
	}
	assert.Len(t, seen, 50)
	assert.False(t, strings.Contains(strings.Join(keysOf(seen), ""), " "))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
