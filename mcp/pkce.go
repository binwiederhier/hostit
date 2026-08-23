// Package mcp is hostit's Model Context Protocol client: it connects to an MCP
// server on an owner's behalf, handles whatever authentication that server
// demands, and calls its tools for the apps -- and the assistant -- that were
// granted it.
//
// hostit speaks MCP rather than handing an app a token and letting it speak for
// itself. That is the opposite of the choice made for every other connection,
// and deliberately: the objection to proxying an API is that you take on a
// surface per vendor forever, and MCP is ONE protocol, so there is exactly one
// implementation regardless of how many servers are connected. It is also the
// only shape in which the built-in assistant can use the tools at all.
package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE is one authorization request's proof key (RFC 7636). The verifier is
// kept by hostit; only the challenge goes through the browser, so a stolen
// authorization code cannot be redeemed by whoever stole it.
//
// OAuth 2.1 -- which is what an MCP server's authorization server implements --
// requires this of every client, confidential ones included.
type PKCE struct {
	Verifier  string
	Challenge string
	Method    string
}

// NewPKCE generates a fresh verifier and its S256 challenge.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 48) // 48 bytes -> 64 base64url characters, inside RFC 7636's 43..128
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		// Never "plain": that sends the verifier through the front channel,
		// which is the thing the exchange exists to keep out of it.
		Method: "S256",
	}, nil
}
