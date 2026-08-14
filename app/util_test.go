package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIDFromHomeDir pins the id extraction every home-derived lookup relies on
// (hostit enter, the assistant sandbox): the account home is the files path
// inside the id-keyed app subvolume, so the id is NOT the basename anymore.
func TestIDFromHomeDir(t *testing.T) {
	t.Parallel()
	// The unified layout: apps/<id>/home/app.
	assert.Equal(t, "1d03bf4f2b2d", IDFromHomeDir("/var/lib/hostit/apps/1d03bf4f2b2d/home/app"))
	assert.Equal(t, "1d03bf4f2b2d", IDFromHomeDir("/var/lib/hostit/apps/1d03bf4f2b2d/home/app/"))
	// A pre-unification account (a login racing the one-time migration) still has
	// the subvolume itself as its home.
	assert.Equal(t, "1d03bf4f2b2d", IDFromHomeDir("/var/lib/hostit/apps/1d03bf4f2b2d"))
}
