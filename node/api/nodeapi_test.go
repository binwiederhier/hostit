package api

import "testing"

// ValidName is the contract both halves enforce (control on create/rename, a
// node on the login users it manages). It has to stay tight: an app name becomes
// a Unix username, a btrfs subvolume path and a systemd unit name, so anything
// it lets through is a name/path-injection primitive.
func TestValidName(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a", // a single letter is the minimum
		"ab",
		"app",
		"my-app",
		"blog2",
		"a1",
		"a234567890123456789012345678901b", // 32 chars: the maximum
	}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",  // empty
		"A", // uppercase: not a safe unix name
		"App",
		"1app",   // must start with a letter
		"-app",   // no leading dash
		"app-",   // no trailing dash
		"my_app", // underscore is not allowed
		"my.app", // a dot is a path/DNS separator
		"my/app", // slash: path injection
		"../etc", // path traversal
		"my app", // whitespace
		"app;rm", // shell metacharacter
		"app$(x)",
		"a2345678901234567890123456789012c", // 33 chars: over the maximum
	}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}
