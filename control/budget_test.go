package control

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/node"
	"heckel.io/hostit/workspace"
)

func TestCreateAppWiresSubvolumeAndBudget(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "257\n")
	a := createTestApp(t, m, "blog")
	pool := m.config.AppsDir
	group := fmt.Sprintf("1/%d", workspace.UIDFor(a.Port))

	// The budget group exists and is capped FIRST, and the app subvolume is
	// snapshotted INTO it (-i): membership is atomic, so the cap enforces from
	// the first byte (a post-hoc assign leaves the group unenforced until a
	// quota rescan completes).
	ran := r.ran()
	assert.Contains(t, ran, "btrfs qgroup create "+group+" "+pool)
	assert.Contains(t, ran, "btrfs subvolume snapshot -i "+group+" "+m.testMachine().Workspace().BasePath(a.ImageTag)+" "+m.testMachine().AppSubvolume("blog"))
	assert.NotContains(t, ran, "chown", "the subvolume stays root-owned for the idmap mount")

	// DiskMB 0 no longer means unlimited: nothing is ever uncapped anymore (an
	// uncapped app once filled the whole host), so 0 falls back to the default.
	assert.Contains(t, ran, fmt.Sprintf("btrfs qgroup limit -e %dM %s %s", node.DefaultDiskCapMB, group, pool))
}

func TestDeleteAppRemovesSubvolumeAndBudget(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	subvol := m.testMachine().AppSubvolume("blog")
	group := fmt.Sprintf("1/%d", workspace.UIDFor(a.Port))
	r.reset()
	require.NoError(t, m.DeleteApp("blog"))
	m.WaitBackground()               // control's half
	m.testMachine().WaitBackground() // the subvolume/qgroup teardown is the node's
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+subvol)
	assert.Contains(t, r.ran(), "btrfs qgroup destroy "+group+" "+m.config.AppsDir)
}

func TestEnableDiskBudgetsMigratesThePoolToSimpleQuotas(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	// The pool still runs full qgroups (an install predating squota): startup
	// migrates it -- disable, then enable simple -- so budgets actually enforce.
	r.returns("findmnt", "abcd-1234\n")
	r.returns("cat /sys/fs/btrfs/abcd-1234/qgroups/mode", "qgroup\n")
	m.testMachine().EnableDiskBudgets()
	assert.Contains(t, r.ran(), "btrfs quota disable "+m.config.AppsDir)
}

func TestSweepStaleQgroups(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "keep")
	liveBudget := fmt.Sprintf("1/%d", workspace.UIDFor(a.Port))
	// The pool has one live subvolume (rootid 256); 0/300 belonged to a deleted
	// snapshot, 1/9999 to a deleted app. 0/5 is the filesystem root, never listed
	// by "subvolume list" but never stale either.
	r.returns("btrfs subvolume list", "ID 256 gen 10 top level 5 path "+a.ID+"\n")
	r.returns("btrfs qgroup show", "Qgroupid Referenced Exclusive Path\n"+
		"-------- ---------- --------- ----\n"+
		"0/5      16.00KiB   16.00KiB  <toplevel>\n"+
		"0/256    1.00MiB    1.00MiB   "+a.ID+"\n"+
		"0/300    1.00MiB    1.00MiB   <stale>\n"+
		liveBudget+"   1.00MiB    1.00MiB   <0 member qgroups>\n"+
		"1/9999   0.00B      0.00B     <0 member qgroups>\n")
	r.reset()
	m.testMachine().SweepStaleQgroups()

	assert.Contains(t, r.ran(), "btrfs qgroup destroy 0/300 "+m.config.AppsDir)
	assert.Contains(t, r.ran(), "btrfs qgroup destroy 1/9999 "+m.config.AppsDir)
	assert.NotContains(t, r.ran(), "destroy 0/256", "a live subvolume's qgroup stays")
	assert.NotContains(t, r.ran(), "destroy "+liveBudget+" ", "a live app's budget stays")
	assert.NotContains(t, r.ran(), "destroy 0/5", "the filesystem root qgroup stays")
}
