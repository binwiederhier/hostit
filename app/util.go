package app

import (
	"fmt"

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
