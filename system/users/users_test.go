package users

import "testing"

func TestUID(t *testing.T) {
	// root is uid 0 on every Linux host.
	if got := UID("root"); got != 0 {
		t.Errorf("UID(root) = %d, want 0", got)
	}
	// A user that does not exist resolves to -1, which callers read as "no such
	// colocated member here".
	if got := UID("hostit-nonexistent-xyz"); got != -1 {
		t.Errorf("UID(absent) = %d, want -1", got)
	}
}
