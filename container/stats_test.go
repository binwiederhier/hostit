package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStats(t *testing.T) {
	t.Parallel()
	// Exactly the shape podman 4.9 prints
	stats, err := ParseStats(`[{"id":"abc","name":"hostit-app-one","cpu_percent":"3.70%","mem_usage":"24.5MB / 536.9MB","pids":"11"},
	                          {"id":"def","name":"not-ours","mem_usage":"99MB / 536.9MB"}]`)
	require.NoError(t, err)
	require.Len(t, stats, 2)
	assert.Equal(t, Stat{Name: "hostit-app-one", MemoryMB: 24, CPUPercent: 4}, stats[0])
	// A line with no cpu_percent still yields its memory; the missing field is zero
	assert.Equal(t, Stat{Name: "not-ours", MemoryMB: 99, CPUPercent: 0}, stats[1])
}

func TestParseStatsSurvivesBrokenOutput(t *testing.T) {
	t.Parallel()
	// A wedged podman can print anything; the caller degrades, it must not crash
	_, err := ParseStats("not json at all")
	require.Error(t, err)
	stats, err := ParseStats("[]")
	require.NoError(t, err)
	assert.Empty(t, stats)
}

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

func TestParseCPUPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"0.00%", 0},
		{"3.70%", 4},
		{"12.4%", 12},
		{"100.00%", 100},
		{"250.5%", 251}, // multi-core containers can exceed 100
		{"", 0},
		{"garbage", 0},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseCPUPercent(tt.in), "input %q", tt.in)
	}
}
