package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBtrfsEnabledDetection(t *testing.T) {
	t.Parallel()
	on, _, onRunner := newTestDeployManager(t)
	onRunner.returns("stat -f", "btrfs\n")
	assert.True(t, on.btrfsEnabled())

	off, _, offRunner := newTestDeployManager(t)
	offRunner.returns("stat -f", "ext2/ext3\n")
	assert.False(t, off.btrfsEnabled())

	// Unreadable (default fake output is empty) means not btrfs, so hostit keeps
	// the plain-directory behavior.
	dunno, _, _ := newTestDeployManager(t)
	assert.False(t, dunno.btrfsEnabled())
}
