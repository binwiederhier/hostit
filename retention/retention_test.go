package retention

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortedIDs returns the ids of a snapshot slice, sorted, for easy assertions.
func sortedIDs(ss []Snapshot) []string {
	ids := make([]string, 0, len(ss))
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

// keptIDs runs the retention and returns the kept ids, sorted.
func keptIDs(snaps []Snapshot, p Policy) []string {
	keep, _ := Apply(snaps, p)
	return sortedIDs(keep)
}

// autoSeries builds n auto snapshots spaced `step` apart, newest last, ending at
// `end`. ids are s0000.. in time order (oldest first).
func autoSeries(end time.Time, step time.Duration, n int) []Snapshot {
	out := make([]Snapshot, n)
	for i := 0; i < n; i++ {
		out[i] = Snapshot{
			ID:        idFor(i),
			App:       "blog",
			CreatedAt: end.Add(-time.Duration(n-1-i) * step),
			Auto:      true,
		}
	}
	return out
}

func idFor(i int) string {
	return "s" + time.Unix(int64(i), 0).UTC().Format("150405") // stable-ish unique-ish
}

func TestDefaultRetentionValues(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Policy{Last: 50, Daily: 7, Weekly: 4, Monthly: 3}, Default)
}

func TestRetentionEmpty(t *testing.T) {
	t.Parallel()
	keep, prune := Apply(nil, Default)
	assert.Empty(t, keep)
	assert.Empty(t, prune)
}

func TestRetentionFewerThanLastKeepsAll(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	snaps := autoSeries(base, time.Hour, 10) // 10 hourly, well under 50
	keep, prune := Apply(snaps, Default)
	assert.Len(t, keep, 10)
	assert.Empty(t, prune)
}

func TestRetentionKeepsLastNWithinOneDay(t *testing.T) {
	t.Parallel()
	// 60 snapshots one minute apart, all the same day: last=50 keeps the newest 50;
	// daily/weekly/monthly all resolve to the same newest snap (already kept).
	base := time.Date(2026, 8, 7, 23, 59, 0, 0, time.UTC)
	snaps := autoSeries(base, time.Minute, 60)
	keep, prune := Apply(snaps, Default)
	assert.Len(t, keep, 50)
	assert.Len(t, prune, 10)
	// the pruned ones are the 10 oldest
	prunedNewest := prune[0]
	for _, p := range prune {
		if p.CreatedAt.After(prunedNewest.CreatedAt) {
			prunedNewest = p
		}
	}
	for _, k := range keep {
		assert.True(t, k.CreatedAt.After(prunedNewest.CreatedAt), "every kept snap is newer than every pruned one")
	}
}

func TestRetentionKeepsNewestPerDay(t *testing.T) {
	t.Parallel()
	// Three snaps on one day; with only daily=1 (no last), keep the newest of the day.
	day := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{ID: "morning", CreatedAt: day.Add(9 * time.Hour), Auto: true},
		{ID: "noon", CreatedAt: day.Add(12 * time.Hour), Auto: true},
		{ID: "evening", CreatedAt: day.Add(20 * time.Hour), Auto: true},
	}
	assert.Equal(t, []string{"evening"}, keptIDs(snaps, Policy{Daily: 1}))
}

func TestRetentionDailyBeyondLast(t *testing.T) {
	t.Parallel()
	// Five days, three snapshots each. With last=2 and daily=5 the two newest survive
	// outright, and the newest of each of the five days survives too -- so the daily
	// tier retains snapshots older than the last-2 window.
	snaps := []Snapshot{}
	for _, d := range []int{3, 4, 5, 6, 7} {
		day := time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC)
		for _, h := range []int{9, 12, 20} {
			snaps = append(snaps, Snapshot{ID: fmt.Sprintf("d%02d-%02d", d, h), CreatedAt: day.Add(time.Duration(h) * time.Hour), Auto: true})
		}
	}
	assert.Equal(t,
		[]string{"d03-20", "d04-20", "d05-20", "d06-20", "d07-12", "d07-20"},
		keptIDs(snaps, Policy{Last: 2, Daily: 5}))
}

func TestRetentionMonthlyExtendsPastLast(t *testing.T) {
	t.Parallel()
	// last=1 keeps only the newest; monthly=3 additionally keeps the newest of the last
	// three months, so months-old snapshots survive -- while the same-month non-newest
	// (aug6), which no tier represents, is pruned.
	snaps := []Snapshot{
		{ID: "aug7", CreatedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "aug6", CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "jul7", CreatedAt: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "jun7", CreatedAt: time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC), Auto: true},
	}
	keep, prune := Apply(snaps, Policy{Last: 1, Monthly: 3})
	assert.Equal(t, []string{"aug7", "jul7", "jun7"}, sortedIDs2(keep))
	assert.Equal(t, []string{"aug6"}, sortedIDs(prune))
}

// sortedIDs2 keeps keep order by time (newest first) for readability in one test.
func sortedIDs2(ss []Snapshot) []string {
	cp := append([]Snapshot(nil), ss...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].CreatedAt.After(cp[j].CreatedAt) })
	ids := make([]string, len(cp))
	for i, s := range cp {
		ids[i] = s.ID
	}
	return ids
}

func TestRetentionWeeklyKeepsNewestPerWeek(t *testing.T) {
	t.Parallel()
	// Four Fridays in four distinct ISO weeks, plus a Thursday sharing the newest
	// week. weekly=4 keeps the newest of each week; the same-week Thursday drops.
	snaps := []Snapshot{
		{ID: "aug7", CreatedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), Auto: true}, // wk A (Fri)
		{ID: "aug6", CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC), Auto: true}, // wk A (Thu)
		{ID: "jul31", CreatedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "jul24", CreatedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "jul17", CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC), Auto: true},
	}
	assert.Equal(t, []string{"aug7", "jul17", "jul24", "jul31"}, keptIDs(snaps, Policy{Weekly: 4}))
}

func TestRetentionPrunesManualSnapshotsToo(t *testing.T) {
	t.Parallel()
	// Manual snapshots are subject to retention like any other: three in one day with
	// last=1 and daily=1 keep only the newest; the older manual ones are pruned.
	day := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{ID: "m-morning", CreatedAt: day.Add(9 * time.Hour), Auto: false, Label: "a"},
		{ID: "m-noon", CreatedAt: day.Add(12 * time.Hour), Auto: false, Label: "b"},
		{ID: "m-evening", CreatedAt: day.Add(20 * time.Hour), Auto: false, Label: "c"},
	}
	keep, prune := Apply(snaps, Policy{Last: 1, Daily: 1})
	assert.Equal(t, []string{"m-evening"}, sortedIDs(keep))
	assert.ElementsMatch(t, []string{"m-morning", "m-noon"}, sortedIDs(prune))
}

func TestRetentionZeroPolicyPrunesAllAuto(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	snaps := autoSeries(base, time.Hour, 5)
	keep, prune := Apply(snaps, Policy{})
	assert.Empty(t, keep)
	assert.Len(t, prune, 5)
}

func TestRetentionDedupAcrossTiers(t *testing.T) {
	t.Parallel()
	// A single snapshot satisfies last, daily, weekly and monthly at once; it must
	// appear exactly once in keep.
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	snaps := []Snapshot{{ID: "only", CreatedAt: base, Auto: true}}
	keep, prune := Apply(snaps, Default)
	require.Len(t, keep, 1)
	assert.Equal(t, "only", keep[0].ID)
	assert.Empty(t, prune)
}

func TestRetentionUnsortedInput(t *testing.T) {
	t.Parallel()
	// Retention must not depend on input order.
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	snaps := autoSeries(base, time.Minute, 60)
	// shuffle deterministically
	for i := range snaps {
		j := (i * 7) % len(snaps)
		snaps[i], snaps[j] = snaps[j], snaps[i]
	}
	keep, prune := Apply(snaps, Default)
	assert.Len(t, keep, 50)
	assert.Len(t, prune, 10)
}

func TestRetentionWeekBucketsAcrossYearBoundary(t *testing.T) {
	t.Parallel()
	// Snapshots straddling the new year: weekly grouping uses ISO week, so late-Dec
	// and early-Jan can share or split weeks correctly. Two snaps a week apart across
	// the boundary, weekly=2 -> both kept.
	snaps := []Snapshot{
		{ID: "dec", CreatedAt: time.Date(2025, 12, 30, 12, 0, 0, 0, time.UTC), Auto: true},
		{ID: "jan", CreatedAt: time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC), Auto: true},
	}
	assert.Equal(t, []string{"dec", "jan"}, keptIDs(snaps, Policy{Weekly: 2}))
}

func TestRetentionMonthlyKeepsNewestPerMonth(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{ID: "aug-early", CreatedAt: base.AddDate(0, 0, -10), Auto: true},
		{ID: "aug-late", CreatedAt: base, Auto: true},
		{ID: "jul", CreatedAt: base.AddDate(0, -1, 0), Auto: true},
		{ID: "jun", CreatedAt: base.AddDate(0, -2, 0), Auto: true},
		{ID: "may", CreatedAt: base.AddDate(0, -3, 0), Auto: true},
	}
	// monthly=3 keeps newest of Aug, Jul, Jun; May drops.
	assert.Equal(t, []string{"aug-late", "jul", "jun"}, keptIDs(snaps, Policy{Monthly: 3}))
}
