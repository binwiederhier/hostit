package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestContainerKeyFromHome pins the fix for the rename bug: the container is
// resolved from the app user's (id-keyed) home directory, NOT its username, so a
// renamed app still enters its own container. It also guards the input.
func TestContainerKeyFromHome(t *testing.T) {
	cases := []struct {
		name string
		home string
		want string
		ok   bool
	}{
		// The real case: the home lives INSIDE the id-keyed app subvolume
		// (apps/<id>/home/app), and the id -- not the basename "app" and not the
		// username -- resolves the container.
		{"unified home", "/var/lib/hostit/apps/1d03bf4f2b2d/home/app", "hostit-app-1d03bf4f2b2d", true},
		// A pre-unification home is the subvolume itself; still resolvable, so a
		// login racing the one-time migration keeps working.
		{"pre-unification home", "/var/lib/hostit/apps/1d03bf4f2b2d", "hostit-app-1d03bf4f2b2d", true},
		// A pre-id app still has a name-keyed home, matching its name-keyed container.
		{"name-keyed home (pre-id)", "/var/lib/hostit/apps/blog", "hostit-app-blog", true},
		{"trailing slash", "/var/lib/hostit/apps/abc123/home/app/", "hostit-app-abc123", true},
		// Guards: nothing argument-shaped may reach podman.
		{"empty", "", "", false},
		{"root", "/", "", false},
		// A ".." is resolved by Clean to the (safe) parent basename, never traversal.
		{"dotdot cleaned", "/var/lib/hostit/apps/..", "hostit-app-hostit", true},
		{"uppercase", "/var/lib/hostit/apps/Blog/home/app", "", false},
		{"flag-shaped", "/var/lib/hostit/apps/--privileged/home/app", "", false},
		{"space", "/var/lib/hostit/apps/a b/home/app", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := containerKeyFromHome(tc.home)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestContainerKeyIgnoresUsername is the pointed regression test: given a renamed
// app (home keyed by id, username the new name), the container must come from the
// id, never from the username. The old code did `hostit-app-<username>` and broke.
func TestContainerKeyIgnoresUsername(t *testing.T) {
	// Home is inside the id-keyed subvolume; the username after a rename would be
	// e.g. "demo23".
	got, ok := containerKeyFromHome("/var/lib/hostit/apps/1d03bf4f2b2d/home/app")
	assert.True(t, ok)
	assert.Equal(t, "hostit-app-1d03bf4f2b2d", got)
	assert.NotEqual(t, "hostit-app-demo23", got, "the container must not be derived from the (renameable) username")
	assert.NotEqual(t, "hostit-app-app", got, "the container must not be derived from the home's basename")
}
