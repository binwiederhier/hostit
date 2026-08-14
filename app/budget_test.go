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
	assert.Contains(t, ran, fmt.Sprintf("chown -R %d:%d %s", m.uidFor(a.Port), m.uidFor(a.Port), m.appSubvolume("blog")))

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
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+subvol)
	assert.Contains(t, r.ran(), "btrfs qgroup destroy "+group+" "+m.config.AppsDir)
}

func TestEnableDiskBudgetsEnablesQuotaOnThePool(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	m.EnableDiskBudgets()
	assert.Contains(t, r.ran(), "btrfs quota enable "+m.config.AppsDir)
}
