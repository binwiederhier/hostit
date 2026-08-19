package control

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/retention"
	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/store"
)

// An archived app is shelved, not deleted: everything that would make it RUN is
// refused, at the one place control reaches a node, so a new verb cannot forget
// to check.
func TestArchivedAppRefusesToRun(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("shelved", nil)
	require.NoError(t, err)
	m.WaitBackground()
	require.NoError(t, m.Archive("shelved"))

	_, err = m.node.Up("shelved")
	assert.ErrorIs(t, err, ErrArchived, "a deploy is refused")
	_, err = m.node.PowerOn("shelved")
	assert.ErrorIs(t, err, ErrArchived, "powering it on is refused")
	_, err = m.node.Ensure("shelved")
	assert.ErrorIs(t, err, ErrArchived, "a login cannot start it")
	assert.ErrorIs(t, m.node.StartApp("shelved"), ErrArchived)
	assert.ErrorIs(t, m.node.Restart("shelved"), ErrArchived)
}

// Stopping and inspecting an archived app still work: refusing those would make
// an app impossible to look at or wind down, which shelving is not about.
func TestArchivedAppCanStillBeStoppedAndInspected(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("shelved", nil)
	require.NoError(t, err)
	m.WaitBackground()
	require.NoError(t, m.Archive("shelved"))

	assert.NotErrorIs(t, m.node.Down("shelved"), ErrArchived, "it can still be powered off")
	assert.NotErrorIs(t, m.node.StopApp("shelved"), ErrArchived)
	_, err = m.node.Status("shelved")
	assert.NotErrorIs(t, err, ErrArchived, "its state is still readable")
}

// Archiving powers the app off, so shelving one actually frees its memory
// rather than leaving it running until something else stops it.
func TestArchivingPowersTheAppOff(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("shelved", nil)
	require.NoError(t, err)
	m.WaitBackground()

	require.NoError(t, m.Archive("shelved"))
	a, err := m.store.App("shelved")
	require.NoError(t, err)
	assert.True(t, a.Archived)
	assert.True(t, a.PoweredOff, "archiving powers it down")

	// Unarchiving leaves it off: coming back from the archive should not start
	// something the owner has not asked to start.
	require.NoError(t, m.Unarchive("shelved"))
	a, err = m.store.App("shelved")
	require.NoError(t, err)
	assert.False(t, a.Archived)
	assert.True(t, a.PoweredOff, "it comes back as an ordinary powered-off app")
}

// An archived app stops taking new snapshots -- it cannot change, so another
// snapshot of it would be a copy of the last one.
func TestArchivedAppsAreSkippedBySweep(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("live", nil)
	require.NoError(t, err)
	_, err = m.CreateApp("shelved", nil)
	require.NoError(t, err)
	makeOverdue(t, m, "live", "shelved")
	require.NoError(t, m.Archive("shelved"))
	agent := &snapAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, agent)

	m.autoSnapshotSweep(time.Now())

	assert.Equal(t, []string{"live"}, agent.takes, "only the live app is snapshotted")
}

// Retention keeps thinning an archived app's history, but never to nothing: the
// monthly rollups survive, because an archived app is one someone may want back
// a year later.
func TestArchivedRetentionKeepsTheMonthlies(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	var snaps []retention.Snapshot
	for i := 0; i < 400; i++ { // over a year of daily snapshots
		snaps = append(snaps, retention.Snapshot{
			ID:        time.Duration(i).String() + "-id",
			App:       "shelved",
			Label:     snapshot.AutoSnapshotLabel,
			CreatedAt: now.Add(-time.Duration(i) * 24 * time.Hour),
			Auto:      true,
		})
	}

	keep, prune := retention.Apply(snaps, retention.Archived)
	assert.NotEmpty(t, prune, "an archived app's history still thins out")
	require.NotEmpty(t, keep, "but it is never pruned to nothing")

	// Distinct months survive, which is what makes a year-old archive useful.
	months := map[string]bool{}
	for _, s := range keep {
		months[s.CreatedAt.Format("2006-01")] = true
	}
	assert.GreaterOrEqual(t, len(months), 6, "monthly rollups are kept, got %d", len(months))
	assert.Less(t, len(keep), 100, "the dense recent history is not kept forever")
}
