package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureDiskUsage(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	group := fmt.Sprintf("1/%d", m.uidFor(a.Port))
	// Usage is the budget group's EXCLUSIVE bytes (the app's true pinned bytes:
	// what deleting it would free), not the referenced count, which would include
	// the ~860 MB base every rootfs shares. 3145728 exclusive bytes is 3 MB.
	r.returns("qgroup show", "Qgroupid Referenced Exclusive Path\n-------- ---------- --------- ----\n0/257 904921088 49152 blog\n"+group+" 904921088 3145728 <2 member qgroups>\n")
	usage, err := m.measureDiskMB("blog")
	require.NoError(t, err)
	assert.Equal(t, 3, usage)
	assert.Contains(t, r.ran(), "btrfs qgroup show --raw "+m.config.AppsDir)
}

func TestSetDiskLimitCapsTheBudgetGroupNotTheHome(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	group := fmt.Sprintf("1/%d", m.uidFor(a.Port))
	r.reset()
	m.SetDiskLimit("blog", 512)
	// The limit is the app's COMBINED budget (home + rootfs + snapshots), enforced
	// as the group's exclusive bytes -- not a per-home qgroup limit anymore.
	assert.Contains(t, r.ran(), "btrfs qgroup limit -e 512M "+group+" "+m.config.AppsDir)
	assert.NotContains(t, r.ran(), "btrfs qgroup limit 512M", "the old per-home referenced limit is gone")
}

func TestSetDiskLimitZeroFallsBackToTheDefaultCap(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	a := createTestApp(t, m, "blog")
	group := fmt.Sprintf("1/%d", m.uidFor(a.Port))
	r.reset()
	m.SetDiskLimit("blog", 0)
	// 0 used to mean unlimited; an uncapped app once dd'd the host full, so now
	// nothing is ever unlimited: 0 maps to the default cap.
	assert.Contains(t, r.ran(), fmt.Sprintf("btrfs qgroup limit -e %dM %s %s", defaultDiskCapMB, group, m.config.AppsDir))
}

// RefreshDiskUsage records usage for the dashboard but never enforces: btrfs
// qgroups hard-cap writes at SetDiskLimit time, so over-quota apps are not stopped.
func TestRefreshDiskUsageRecordsUsageWithoutStopping(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	app := createTestApp(t, m, "blog")
	group := fmt.Sprintf("1/%d", m.uidFor(app.Port))
	// The app's budget group reports ~3 MB exclusive (3145728 bytes).
	runner.returns("qgroup show", group+" 904921088 3145728 <2 member qgroups>\n")
	m.SetDiskLimit("blog", 1) // 1 MB limit, app uses ~3 MB
	runner.reset()
	require.NoError(t, m.RefreshDiskUsage())
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, a.DiskMB, 3)                                     // usage recorded
	assert.NotContains(t, runner.ran(), "systemctl stop "+m.unitName("blog")) // never stopped
}
