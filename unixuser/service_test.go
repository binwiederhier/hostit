package unixuser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
