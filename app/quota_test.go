package app

import (
	"os"
	"path/filepath"
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

func TestCheckQuotasStopsOverQuotaApp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "big.bin", strings.Repeat("x", 3*1024*1024))
	m.SetDiskLimit("blog", 1) // 1 MB limit, app uses ~3 MB
	runner.reset()
	require.NoError(t, m.CheckQuotas())
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.True(t, a.OverQuota)
	assert.GreaterOrEqual(t, a.DiskMB, 3)
	assert.Contains(t, runner.ran(), "systemctl stop hostit-app@blog")
}

func TestCheckQuotasLeavesCompliantAppRunning(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "small.bin", "hello")
	m.SetDiskLimit("blog", 100)
	runner.reset()
	require.NoError(t, m.CheckQuotas())
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.False(t, a.OverQuota)
	assert.NotContains(t, runner.ran(), "stop")
}

func TestCheckQuotasClearsFlagWhenCleanedUp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "big.bin", strings.Repeat("x", 3*1024*1024))
	m.SetDiskLimit("blog", 1)
	runner.reset()
	require.NoError(t, m.CheckQuotas())
	a, err := m.App("blog")
	require.NoError(t, err)
	require.True(t, a.OverQuota)
	// User cleans up; the next check clears the flag
	require.NoError(t, os.Remove(filepath.Join(m.appHome("blog"), "big.bin")))
	runner.reset()
	require.NoError(t, m.CheckQuotas())
	a, err = m.App("blog")
	require.NoError(t, err)
	assert.False(t, a.OverQuota)
}

func TestCheckQuotasWithoutLimitIsNoOp(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	writeAppFile(t, m, "blog", "big.bin", strings.Repeat("x", 3*1024*1024))
	runner.reset()
	require.NoError(t, m.CheckQuotas()) // No limit set for this app
	a, err := m.App("blog")
	require.NoError(t, err)
	assert.False(t, a.OverQuota)
	assert.GreaterOrEqual(t, a.DiskMB, 3) // Usage is still measured
	assert.NotContains(t, runner.ran(), "stop")
}
