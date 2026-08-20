package unixuser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupNeedsRename(t *testing.T) {
	t.Parallel()
	// A group left under the old app name (an app renamed before group-rename
	// shipped) must be renamed to match the user, or a new app of the old name
	// collides on groupadd.
	got, ok := groupNeedsRename("demo", "blogapp", "hostit-apps")
	assert.True(t, ok)
	assert.Equal(t, "blogapp", got)

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

// Moving the login shell is a lockout if done wrong: /etc/passwd names the old
// path for every existing app user, and a shell that does not exist refuses
// SSH outright. The selection therefore picks only users on the OLD path --
// never a human's shell, never an already-migrated app -- and the caller
// verifies the new file exists before running a single usermod.
func TestSweepTargetsPickOnlyOldShellEntries(t *testing.T) {
	t.Parallel()
	passwd := strings.NewReader(strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"blog:x:1000000:988:hostit app:/var/lib/hostit/apps/blog:/usr/bin/hostit-shell",
		"wiki:x:1010000:988:hostit app:/var/lib/hostit/apps/wiki:/usr/bin/hostit-shell",
		"done:x:1020000:988:hostit app:/var/lib/hostit/apps/done:/usr/lib/hostit/bin/hostit-shell",
		"phil:x:1001:1001:me:/home/phil:/bin/zsh",
		"broken line without fields",
	}, "\n"))

	targets, err := sweepTargets(passwd, "/usr/bin/hostit-shell")
	require.NoError(t, err)
	assert.Equal(t, []string{"blog", "wiki"}, targets)
}
