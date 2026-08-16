package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

// The Manager delegates snapshots and rollback to the snapshot service; this
// checks the wiring end to end through the real Manager: a rollback takes a
// labelled safety snapshot and brings the app back up via the snapshot.Host.Up
// callback (m.up), driving real systemd/container commands through the fake runner.
// The snapshot service's own behavior is covered in the snapshot package.
func TestManagerRollbackDelegatesAndBringsAppUp(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.failOn("container inspect", assert.AnError) // no container yet -> Up creates one
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.AppFiles("blog").Path(), 0o755))
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")

	target, err := m.TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	require.NoError(t, m.Rollback("blog", target.ID))

	// A labelled safety snapshot was recorded through the delegation.
	snaps, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	want := "Before rolling back to snapshot " + target.ID
	var safety *store.Snapshot
	for _, s := range snaps {
		if s.Label == want {
			safety = s
		}
	}
	require.NotNil(t, safety, "rollback must take a labelled safety snapshot")
	assert.True(t, safety.Auto)

	// The app was brought back up after the rollback (Host.Up -> m.up).
	assert.Contains(t, r.ran(), "systemctl enable --now "+m.UnitName("blog"), "the app is brought back up after rollback")
}

// Snapshot subvolumes are created straight into the app's budget group (-i,
// resolved through the Host.BudgetGroup callback), or home bytes shared with
// the snapshot would stop counting as the group's exclusive.
func TestTakeSnapshotCreatesTheSubvolumeInsideTheBudget(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("inspect-internal rootid", "300\n")
	a := createTestApp(t, m, "blog")
	r.reset()
	snap, err := m.TakeSnapshot("blog", "save", false)
	require.NoError(t, err)
	// Created INTO the budget group (-i): atomic membership, no post-hoc assign
	// (which would leave the group unenforced until a rescan).
	assert.Contains(t, r.ran(), fmt.Sprintf("btrfs subvolume snapshot -r -i 1/%d %s %s", workspace.UIDFor(m.config.PortMin, a.Port), m.AppSubvolume("blog"), m.SnapshotPath("blog", snap.ID)))
	assert.NotContains(t, r.ran(), "qgroup assign")
}
