package app

import (
	"testing"
	"time"

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

func TestSubvolumeCommands(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)

	assert.NoError(t, m.createSubvolume("/apps/blog"))
	assert.Contains(t, r.ran(), "btrfs subvolume create /apps/blog")

	r.reset()
	assert.NoError(t, m.snapshotSubvolume("/apps/blog", "/apps/.snapshots/blog/x", true))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot -r /apps/blog /apps/.snapshots/blog/x")

	r.reset()
	assert.NoError(t, m.snapshotSubvolume("/apps/.snapshots/blog/x", "/apps/blog", false))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot /apps/.snapshots/blog/x /apps/blog")
	assert.NotContains(t, r.ran(), "-r", "a rollback/fork copy is writable")

	r.reset()
	assert.NoError(t, m.deleteSubvolume("/apps/blog"))
	assert.Contains(t, r.ran(), "btrfs subvolume delete /apps/blog")
}

func TestSetQuota(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)

	assert.NoError(t, m.setQuota("/apps/blog", 512))
	assert.Contains(t, r.ran(), "btrfs qgroup limit 512M /apps/blog")

	r.reset()
	assert.NoError(t, m.setQuota("/apps/blog", 0))
	assert.Contains(t, r.ran(), "btrfs qgroup limit none /apps/blog")
}

func TestParseQgroupReferencedMB(t *testing.T) {
	t.Parallel()
	out := `Qgroupid         Referenced    Exclusive  Path
--------         ----------    ---------  ----
0/257            134217728     134217728  blog
`
	assert.Equal(t, 128, parseQgroupReferencedMB(out)) // 134217728 bytes = 128 MB
	assert.Equal(t, 0, parseQgroupReferencedMB(""))
	assert.Equal(t, 0, parseQgroupReferencedMB("garbage\nno rows here"))
}

func TestSubvolumeUsageMB(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("btrfs qgroup show", `Qgroupid  Referenced  Exclusive  Path
--------  ----------  ---------  ----
0/258     268435456   268435456  blog
`)
	assert.Equal(t, 256, m.subvolumeUsageMB("/apps/blog"))
}

func TestSnapshotID(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 8, 7, 14, 5, 1, 0, time.UTC)
	assert.Equal(t, "20260807-140501-auto", snapshotID(ts, "auto"))
}
