package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureDiskUsage(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	// ~3 MB of files in the app home
	big := strings.Repeat("x", 1024*1024)
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		writeAppFile(t, m, "blog", name, big)
	}
	usage, err := m.measureDiskMB("blog")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, usage, 3)
	assert.Less(t, usage, 10) // Sanity: not wildly off
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
	writeAppFile(t, m, "blog", "big.bin", strings.Repeat("x", 3*1024*1024))
	m.SetDiskLimit("blog", 1) // 1 MB limit, app uses ~3 MB
	runner.reset()
	require.NoError(t, m.RefreshDiskUsage())
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, a.DiskMB, 3)                                     // usage recorded
	assert.NotContains(t, runner.ran(), "systemctl stop "+m.unitName("blog")) // never stopped
}
