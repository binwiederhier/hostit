package container

import "regexp"

// nameRegex is the safe form for a podman container name/key: lowercase
// alphanumerics and dashes, starting alphanumeric, bounded in length.
var nameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidName reports whether s is a safe podman container name/key, so an untrusted
// value (e.g. a basename read from a passwd entry) cannot be shaped into podman
// arguments.
func ValidName(s string) bool {
	return nameRegex.MatchString(s)
}
