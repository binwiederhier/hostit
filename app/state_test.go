package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMemMB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"12.3MB / 512MB", 12},
		{"1.5GB / 2GB", 1536},
		{"800kB / 512MB", 0},
		{"512B", 0},
		{"", 0},
		{"garbage", 0},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseMemMB(tt.in), "input %q", tt.in)
	}
}

func TestStatesReadsRunningAndMemory(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	createTestApp(t, m, "two")
	// systemctl prints one line per unit, in the order asked
	runner.returns("systemctl is-active", "active\ninactive\n")
	// Exactly the shape podman 4.9 prints
	runner.returns("podman stats", `[{"id":"abc","name":"hostit-app-one","cpu_percent":"0.00%","mem_usage":"24.5MB / 536.9MB","pids":"11"},
	                                 {"id":"def","name":"not-ours","mem_usage":"99MB / 536.9MB"}]`)
	states := m.States([]string{"one", "two"})
	require.Len(t, states, 2)
	assert.True(t, states["one"].Running)
	assert.False(t, states["two"].Running, "the second unit was inactive")
	assert.Equal(t, 24, states["one"].MemoryMB)
	assert.Equal(t, 0, states["two"].MemoryMB, "no stats line means no usage")
}

func TestStatesWithNoApps(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestDeployManager(t)
	assert.Empty(t, m.States(nil))
}

func TestStatesSurvivesBrokenOutput(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "one")
	runner.returns("podman stats", "not json at all")
	states := m.States([]string{"one"})
	// A broken stats call must not lose the running state or crash the listing
	require.Len(t, states, 1)
	assert.Equal(t, 0, states["one"].MemoryMB)
}
