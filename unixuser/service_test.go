package unixuser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGroupNeedsRename(t *testing.T) {
	t.Parallel()
	// A group left under the old app name (an app renamed before group-rename
	// shipped) must be renamed to match the user, or a new app of the old name
	// collides on groupadd.
	got, ok := groupNeedsRename("demo", "thatphilguy", "hostit-apps")
	assert.True(t, ok)
	assert.Equal(t, "thatphilguy", got)

	// Already matching the user: nothing to do.
	_, ok = groupNeedsRename("blog", "blog", "hostit-apps")
	assert.False(t, ok)

	// The shared app group is not an app's own group; never touch it.
	_, ok = groupNeedsRename("hostit-apps", "blog", "hostit-apps")
	assert.False(t, ok)

	// No group resolved (empty lookup): nothing to do.
	_, ok = groupNeedsRename("", "blog", "hostit-apps")
	assert.False(t, ok)
}

func TestSetHomeArgsChangeOnlyThePasswdEntry(t *testing.T) {
	t.Parallel()
	// The storage-unification migration moves the files itself (reflink copy),
	// so usermod must only rewrite the passwd entry -- --move-home would race the
	// migration's own copy and double-move the data.
	args := setHomeArgs("blog", "/var/lib/hostit/apps/1d03bf4f2b2d/home/app")
	joined := strings.Join(args, " ")
	assert.Equal(t, "usermod", args[0])
	assert.Contains(t, joined, "--home /var/lib/hostit/apps/1d03bf4f2b2d/home/app")
	assert.NotContains(t, joined, "--move-home")
	assert.Equal(t, "blog", args[len(args)-1])
}

func TestCreateUserArgsBringNoSkeleton(t *testing.T) {
	t.Parallel()
	// useradd copies /etc/skel into every new home: .bashrc, .profile,
	// .bash_logout, .cloud-locale-test.skip. None of that is the app's, and an
	// app directory should hold the app's files and hostit's own, nothing else.
	args := createUserArgs("blog", "/var/lib/hostit/apps/blog", 1000000, "/usr/bin/hostit-shell", "hostit-apps")
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "--no-create-home")
	assert.NotContains(t, joined, "--create-home")
	assert.Contains(t, joined, "--home-dir /var/lib/hostit/apps/blog")
	assert.Contains(t, joined, "--shell /usr/bin/hostit-shell")
	assert.Contains(t, joined, "--groups hostit-apps")
	// Pinned to its contiguous id block
	assert.Contains(t, joined, "--uid 1000000")
	assert.Contains(t, joined, "--gid 1000000")
}
