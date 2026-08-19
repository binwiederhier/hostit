package control

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of staggering: a fleet's snapshots must not all land on the same
// tick. Walking a whole interval, every app fires exactly once, and no single
// tick carries them all.
func TestSnapshotsSpreadAcrossTheInterval(t *testing.T) {
	t.Parallel()
	const interval = 3 * time.Hour
	apps := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		apps = append(apps, fmt.Sprintf("app-%d", i))
	}

	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	fired := make(map[string]int)
	busiest := 0
	for tick := time.Duration(0); tick < interval; tick += snapshotTick {
		now := start.Add(tick)
		this := 0
		for _, name := range apps {
			// Last snapshot long enough ago that only the slot decides.
			if snapshotDue(name, interval, now.Add(-interval-time.Minute), now) {
				fired[name]++
				this++
			}
		}
		if this > busiest {
			busiest = this
		}
	}

	for _, name := range apps {
		assert.Equalf(t, 1, fired[name], "%s should snapshot exactly once per interval", name)
	}
	slots := int(interval / snapshotTick)
	assert.LessOrEqual(t, busiest, len(apps)/slots+2, "no tick should carry the whole fleet")
	assert.Greater(t, slots, 1, "there must be more than one slot to spread across")
}

// A snapshot taken recently means the app is not due, whatever its slot says.
func TestSnapshotNotDueBeforeItsInterval(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for tick := time.Duration(0); tick < 3*time.Hour; tick += snapshotTick {
		at := now.Add(tick)
		assert.False(t, snapshotDue("blog", 3*time.Hour, at.Add(-time.Minute), at),
			"an app snapshotted a minute ago is never due")
	}
}

// Control being down through an app's slot must not push it to the back of the
// queue: once it is badly overdue it goes on the next tick, slot or not.
func TestAnOverdueAppDoesNotWaitForItsSlot(t *testing.T) {
	t.Parallel()
	const interval = time.Hour
	now := time.Date(2026, 8, 19, 7, 13, 0, 0, time.UTC)
	require.False(t, snapshotDue("quiet", interval, now.Add(-interval-time.Second), now),
		"this app's slot is not now (the case the next assert relies on)")
	assert.True(t, snapshotDue("quiet", interval, now.Add(-3*interval), now),
		"an app three intervals behind snapshots at the next opportunity")
}

// An interval of zero is the app opting out, and nothing makes it fire.
func TestZeroIntervalNeverSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	assert.False(t, snapshotDue("archive", 0, time.Time{}, now), "never snapshotted, still opted out")
	assert.False(t, snapshotDue("archive", 0, now.Add(-100*time.Hour), now))
}

// An app that has never been snapshotted is due, but still in its own slot, so
// a fresh control does not snapshot every app it has at once.
func TestNeverSnapshottedIsDueInItsSlot(t *testing.T) {
	t.Parallel()
	const interval = 3 * time.Hour
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	fired := 0
	for tick := time.Duration(0); tick < interval; tick += snapshotTick {
		if snapshotDue("brand-new", interval, time.Time{}, start.Add(tick)) {
			fired++
		}
	}
	assert.Equal(t, 1, fired, "a never-snapshotted app fires once in the interval, not on every tick")
}
