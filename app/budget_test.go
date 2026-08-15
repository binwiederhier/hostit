package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAppWiresSubvolumeAndBudget(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "257\n")
	a := createTestApp(t, m, "blog")
	pool := m.config.AppsDir
	group := fmt.Sprintf("1/%d", m.uidFor(a.Port))

	// The app subvolume is a writable snapshot of the base, chowned to the app's
	// block.
	ran := r.ran()
	assert.Contains(t, ran, "btrfs subvolume snapshot "+m.workspace.BasePath(a.ImageTag)+" "+m.appSubvolume("blog"))
	assert.NotContains(t, ran, "chown", "the subvolume stays root-owned for the idmap mount")

	// The budget group is keyed on the app's uid and the ONE subvolume joins it:
	// files, installed software and (later) snapshots share one cap.
	assert.Contains(t, ran, "btrfs qgroup create "+group+" "+pool)
	assert.Contains(t, ran, "btrfs inspect-internal rootid "+m.appSubvolume("blog"))
	assert.Contains(t, ran, "btrfs qgroup assign 0/257 "+group+" "+pool)

	// DiskMB 0 no longer means unlimited: nothing is ever uncapped anymore (an
	// uncapped app once filled the whole host), so 0 falls back to the default.
	assert.Contains(t, ran, fmt.Sprintf("btrfs qgroup limit -e %dM %s %s", defaultDiskCapMB, group, pool))
}

func TestDeleteAppRemovesSubvolumeAndBudget(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	subvol := m.appSubvolume("blog")
	group := fmt.Sprintf("1/%d", m.uidFor(a.Port))
	r.reset()
	require.NoError(t, m.DeleteApp("blog"))
	m.background.Wait() // the subvolume/qgroup teardown runs in the background
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+subvol)
	assert.Contains(t, r.ran(), "btrfs qgroup destroy "+group+" "+m.config.AppsDir)
}

func TestEnableDiskBudgetsEnablesQuotaOnThePool(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	m.EnableDiskBudgets()
	assert.Contains(t, r.ran(), "btrfs quota enable "+m.config.AppsDir)
}

func TestSweepStaleQgroups(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "keep")
	liveBudget := fmt.Sprintf("1/%d", m.uidFor(a.Port))
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
	m.SweepStaleQgroups()

	assert.Contains(t, r.ran(), "btrfs qgroup destroy 0/300 "+m.config.AppsDir)
	assert.Contains(t, r.ran(), "btrfs qgroup destroy 1/9999 "+m.config.AppsDir)
	assert.NotContains(t, r.ran(), "destroy 0/256", "a live subvolume's qgroup stays")
	assert.NotContains(t, r.ran(), "destroy "+liveBudget+" ", "a live app's budget stays")
	assert.NotContains(t, r.ran(), "destroy 0/5", "the filesystem root qgroup stays")
}
