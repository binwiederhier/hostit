package stats

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// "Used" is what a human means by it: total minus AVAILABLE, not total minus
// free. Linux keeps page cache in "used" territory that it hands back on
// demand, so total-minus-free reads as "97% used" on a healthy idle box and
// would have every member looking permanently on fire.
func TestParseMeminfo(t *testing.T) {
	t.Parallel()
	const meminfo = `MemTotal:        1010900 kB
MemFree:          104736 kB
MemAvailable:     612344 kB
Buffers:           38104 kB
Cached:           480200 kB
`
	total, available, err := parseMeminfo(strings.NewReader(meminfo))
	require.NoError(t, err)
	assert.Equal(t, 987, total, "kB rounded to MB")
	assert.Equal(t, 597, available, "integer MB, truncated")

	_, _, err = parseMeminfo(strings.NewReader("nothing useful here\n"))
	assert.Error(t, err, "a meminfo without MemTotal is not a meminfo")
}

func TestParseLoadavg(t *testing.T) {
	t.Parallel()
	load, err := parseLoadavg(strings.NewReader("0.52 0.31 0.24 1/243 12345\n"))
	require.NoError(t, err)
	assert.InDelta(t, 0.52, load, 0.001)

	_, err = parseLoadavg(strings.NewReader(""))
	assert.Error(t, err)
}

// The percentages are what the UI colours on, so they must not divide by zero
// on a member whose stats never arrived.
func TestPercentagesAreSafeWhenEmpty(t *testing.T) {
	t.Parallel()
	var zero Stats
	assert.Zero(t, zero.MemoryPercent())
	assert.Zero(t, zero.DiskPercent())
	assert.False(t, zero.Known(), "an all-zero Stats is 'never reported', not 'an idle machine'")

	s := Stats{MemoryUsedMB: 512, MemoryTotalMB: 1024, DiskUsedMB: 30, DiskTotalMB: 100}
	assert.Equal(t, 50, s.MemoryPercent())
	assert.Equal(t, 30, s.DiskPercent())
	assert.True(t, s.Known())
}
