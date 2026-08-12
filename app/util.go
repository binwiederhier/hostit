package app

import (
	"fmt"

	"heckel.io/hostit/ssh"
)

// ValidName reports whether s is a valid hostit app name (safe as a Unix username
// and a DNS label). Format only -- CreateApp additionally rejects reserved names
// and duplicates.
func ValidName(s string) bool {
	return appNameRegex.MatchString(s)
}

// validateKeys ensures every entry is a parseable authorized_keys line, wrapping
// the ssh package's check in ErrInvalid so the server reports it as a bad request.
func validateKeys(keys []string) error {
	if err := ssh.ValidateKeys(keys); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return nil
}
