package control

import (
	"hash/fnv"
	"time"
)

// When automatic snapshots happen. Hourly snapshots of every app on the same
// tick spiked the pool and the cleaner together; apps now default to every
// three hours (app.DefaultSnapshotInterval) and are spread across that window
// so the load is flat rather than bursty.

const (
	// snapshotTick is how often the sweep wakes up, and the resolution of the
	// staggering: an interval is divided into this many slots and each app fires
	// in exactly one of them. Finer would spread a large fleet better; coarser
	// costs less. Fifteen minutes gives a 3h interval twelve slots.
	snapshotTick = 15 * time.Minute
	// overdueFactor is how far past its interval an app may drift before its
	// slot stops mattering. Without this an app whose slot passed while control
	// was down would wait out another full interval.
	overdueFactor = 2
)

// snapshotDue decides whether an app should be snapshotted on this tick: it is
// past due, and this tick is the app's own slot within the interval. An app
// that has never been snapshotted is past due by definition but still waits for
// its slot, so a control that has just started does not snapshot everything it
// hosts at once.
func snapshotDue(name string, interval time.Duration, last, now time.Time) bool {
	if interval <= 0 {
		return false // the app opted out
	}
	if !last.IsZero() {
		age := now.Sub(last)
		if age < interval {
			return false
		}
		if age >= overdueFactor*interval {
			return true // so far behind that waiting for the slot would skip another interval
		}
	}
	// Due, and this is the app's slot. A never-snapshotted app arrives here too:
	// due by definition, but still spread out rather than firing immediately.
	return snapshotSlot(name, interval) == currentSlot(interval, now)
}

// snapshotSlot is the app's fixed position within the interval, from a hash of
// its name: stable across restarts (so an app keeps its slot) and unrelated to
// creation order (so apps made in a batch do not share one).
func snapshotSlot(name string, interval time.Duration) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum32()) % slotCount(interval)
}

// currentSlot is which slot of the interval the clock is in now. It counts from
// the epoch rather than from process start, so a restart does not shift every
// app's slot and re-bunch the fleet.
func currentSlot(interval time.Duration, now time.Time) int64 {
	tick := int64(snapshotTick / time.Second)
	elapsed := now.Unix() % int64(interval/time.Second)
	return elapsed / tick
}

// slotCount is how many ticks fit in the interval, at least one (an interval
// shorter than a tick simply fires every tick).
func slotCount(interval time.Duration) int64 {
	n := int64(interval / snapshotTick)
	if n < 1 {
		return 1
	}
	return n
}
