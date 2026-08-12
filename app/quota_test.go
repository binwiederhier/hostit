package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureDiskUsage(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// Usage is read from the subvolume's qgroup (btrfs qgroup show), not a walk:
	// 3145728 referenced bytes is 3 MB.
	r.returns("qgroup show", "Qgroupid Referenced Exclusive Path\n-------- ---------- --------- ----\n0/257 3145728 3145728 blog\n")
	usage, err := m.measureDiskMB("blog")
	require.NoError(t, err)
	assert.Equal(t, 3, usage)
}

func TestCreateAppSetsDiskQuotaOnBtrfs(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	_, err := m.CreateApp("blog", &CreateOptions{DiskMB: 512})
	require.NoError(t, err)
	// The hard qgroup cap is applied at create, not only at the next daemon restart.
	assert.Contains(t, r.ran(), "btrfs qgroup limit 512M "+m.appHome("blog"))
}

// RefreshDiskUsage records usage for the dashboard but never enforces: btrfs
// qgroups hard-cap writes at SetDiskLimit time, so over-quota apps are not stopped.
func TestRefreshDiskUsageRecordsUsageWithoutStopping(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// The app's qgroup reports ~3 MB referenced (3145728 bytes).
	runner.returns("qgroup show", "Qgroupid Referenced Exclusive Path\n0/257 3145728 3145728 blog\n")
	m.SetDiskLimit("blog", 1) // 1 MB limit, app uses ~3 MB
	runner.reset()
	require.NoError(t, m.RefreshDiskUsage())
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, a.DiskMB, 3)                                     // usage recorded
	assert.NotContains(t, runner.ran(), "systemctl stop "+m.unitName("blog")) // never stopped
}
