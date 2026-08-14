package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"heckel.io/hostit/ssh"
	"heckel.io/hostit/workspace"
)

// ValidName reports whether s is a valid hostit app name (safe as a Unix username
// and a DNS label). Format only -- CreateApp additionally rejects reserved names
// and duplicates.
func ValidName(s string) bool {
	return appNameRegex.MatchString(s)
}

// IDFromHomeDir extracts the app id from an app account's home directory. The
// account home is the files path inside the id-keyed app subvolume
// (<apps>/<id>/home/app), so the id sits above the home/app tail; a
// pre-unification account (e.g. a login racing the one-time migration) still
// has <apps>/<id> itself, whose basename is already the id.
func IDFromHomeDir(home string) string {
	clean := filepath.Clean(home)
	if rest, ok := strings.CutSuffix(clean, "/"+workspace.FilesDir); ok {
		return filepath.Base(rest)
	}
	return filepath.Base(clean)
}

// validateKeys ensures every entry is a parseable authorized_keys line, wrapping
// the ssh package's check in ErrInvalid so the server reports it as a bad request.
func validateKeys(keys []string) error {
	if err := ssh.ValidateKeys(keys); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return nil
}
