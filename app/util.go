package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// validateKeys ensures every entry is a parseable authorized_keys line
func validateKeys(keys []string) error {
	for _, key := range keys {
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key)); err != nil {
			return fmt.Errorf("%w: invalid ssh key %q: %s", ErrInvalid, key, err.Error())
		}
	}
	return nil
}

// generateKeyPair creates an ed25519 SSH key pair in OpenSSH format
func generateKeyPair(comment string) (*Credentials, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment
	return &Credentials{
		PrivateKey: string(pem.EncodeToMemory(block)),
		PublicKey:  publicKey,
	}, nil
}
