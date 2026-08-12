// Package retention decides which of an app's snapshots to keep and which to
// prune, under a restic-style grandfather-father-son policy. It is pure logic (no
// I/O), so the tricky bucketing math is easy to test.
package retention

import (
	"fmt"
	"sort"
	"time"
)

// Snapshot is one point-in-time copy of an app's home (a read-only btrfs
// subvolume). Auto records how it was taken -- automatically (before a deploy or
// assistant turn, and hourly) or manually (a labelled save the owner/agent asked
// for). Retention applies to all of them, so no snapshot lives forever.
type Snapshot struct {
	ID        string    // Unique, sortable; the subvolume's directory name
	App       string    // The app it belongs to
	Label     string    // Optional human note; empty for automatic snapshots
	CreatedAt time.Time // When it was taken
	Auto      bool      // Taken automatically (retention applies) vs. on purpose
}

// Policy is a restic-style grandfather-father-son policy: keep the most recent
// Last snapshots, plus the newest snapshot in each of the last Daily days, Weekly
// ISO weeks and Monthly months. The union is kept; the rest are pruned.
type Policy struct {
	Last    int
	Daily   int
	Weekly  int
	Monthly int
}

// Default keeps a dense recent history and thins it out with age.
var Default = Policy{Last: 50, Daily: 7, Weekly: 4, Monthly: 3}

// Apply partitions snapshots into those to keep and those to prune under the
// policy. Every snapshot -- manual and automatic alike -- is subject to it, so
// none lives forever. Input need not be sorted; the result is order-independent.
func Apply(snaps []Snapshot, p Policy) (keep, prune []Snapshot) {
	all := append([]Snapshot(nil), snaps...)
	// Newest first, deterministic on ties so pruning is stable.
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	kept := make(map[string]bool, len(all))
	// keep-last: the newest N outright.
	for i := 0; i < p.Last && i < len(all); i++ {
		kept[all[i].ID] = true
	}
	// keep-daily/weekly/monthly: the newest snapshot of each of the last N buckets.
	markBuckets(all, p.Daily, dayBucket, kept)
	markBuckets(all, p.Weekly, weekBucket, kept)
	markBuckets(all, p.Monthly, monthBucket, kept)

	for _, s := range all {
		if kept[s.ID] {
			keep = append(keep, s)
		} else {
			prune = append(prune, s)
		}
	}
	return keep, prune
}

// markBuckets walks snapshots newest-first and keeps the first (newest) snapshot of
// each of the first n distinct buckets, where bucket() names a snapshot's day, week
// or month.
func markBuckets(sortedNewestFirst []Snapshot, n int, bucket func(time.Time) string, kept map[string]bool) {
	if n <= 0 {
		return
	}
	prev := ""
	seen := 0
	for _, s := range sortedNewestFirst {
		b := bucket(s.CreatedAt)
		if b == prev {
			continue // an older snapshot in a bucket we already handled
		}
		prev = b
		if seen >= n {
			return // enough buckets kept for this tier
		}
		kept[s.ID] = true
		seen++
	}
}

// Bucket keys are computed in UTC so retention is deterministic and independent of
// the server's local timezone.
func dayBucket(t time.Time) string { return t.UTC().Format("2006-01-02") }

func weekBucket(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

func monthBucket(t time.Time) string { return t.UTC().Format("2006-01") }
