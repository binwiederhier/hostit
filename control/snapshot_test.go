package control

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/retention"
	"heckel.io/hostit/snapshot"
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
	assert.Contains(t, r.ran(), fmt.Sprintf("btrfs subvolume snapshot -r -i 1/%d %s %s", workspace.UIDFor(a.Port), m.AppSubvolume("blog"), m.SnapshotPath("blog", snap.ID)))
	assert.NotContains(t, r.ran(), "qgroup assign")
}

// snapAgent records the snapshot verbs control sends to the node agent, so
// tests can assert WHAT control decided without a real machine doing the work.
type snapAgent struct {
	NodeAgent
	takes   []string            // app names snapshotted, in order
	labels  []string            // labels sent along
	autos   []bool              // auto flags sent along
	deletes map[string][]string // app -> snapshot ids control asked to delete
	failFor string              // app whose node is "unreachable": TakeSnapshot errors
}

func (a *snapAgent) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	if name == a.failFor {
		return nil, assert.AnError
	}
	a.takes = append(a.takes, name)
	a.labels = append(a.labels, label)
	a.autos = append(a.autos, auto)
	return &store.Snapshot{ID: "snap-" + name, AppName: name, Label: label, Auto: auto}, nil
}

func (a *snapAgent) DeleteSnapshot(name, id string) error {
	if a.deletes == nil {
		a.deletes = make(map[string][]string)
	}
	a.deletes[name] = append(a.deletes[name], id)
	return nil
}

// The hourly automatic snapshot is a CONTROL decision: the sweep walks the
// registry and commands each app's node through the node agent, so a node
// never snapshots on its own timer (and a snapshot can never happen while
// control is away to lose its record).
func TestAutoSnapshotSweepSnapshotsEveryAppThroughTheNodeAgent(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("one", nil)
	require.NoError(t, err)
	_, err = m.CreateApp("two", nil)
	require.NoError(t, err)
	agent := &snapAgent{NodeAgent: m}
	m.SetNodeAgent(agent)

	m.autoSnapshotSweep()

	assert.ElementsMatch(t, []string{"one", "two"}, agent.takes)
	for i := range agent.takes {
		assert.Equal(t, snapshot.AutoSnapshotLabel, agent.labels[i])
		assert.True(t, agent.autos[i], "the sweep takes automatic snapshots")
	}
}

// An unreachable node must not stall or abort the sweep: its app is skipped
// (no snapshot happens, so no record can be lost) and every other app still
// gets its snapshot.
func TestAutoSnapshotSweepSkipsUnreachableApps(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("one", nil)
	require.NoError(t, err)
	_, err = m.CreateApp("two", nil)
	require.NoError(t, err)
	agent := &snapAgent{NodeAgent: m, failFor: "one"}
	m.SetNodeAgent(agent)

	m.autoSnapshotSweep()

	assert.Equal(t, []string{"two"}, agent.takes)
	assert.Empty(t, agent.deletes["one"], "control must not prune an app it could not snapshot")
}

// Retention is a CONTROL decision too: control applies the policy to the
// registry's records and commands the deletions; the node no longer prunes
// after a take.
func TestPruneSnapshotsDeletesBeyondRetentionThroughTheAgent(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("blog", nil)
	require.NoError(t, err)
	// Seed hourly automatic snapshots far past the policy's dense window, so
	// retention.Apply must prune some of them.
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 80; i++ {
		require.NoError(t, m.store.AddSnapshot(&store.Snapshot{
			ID:        fmt.Sprintf("20260801-%06d-abc", i),
			AppName:   "blog",
			Label:     snapshot.AutoSnapshotLabel,
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
			Auto:      true,
		}))
	}
	// The expectation comes from the registry's actual rows (the create's own
	// background deploy adds a pre-deploy snapshot too); the policy function
	// itself is covered in the retention package.
	rows, err := m.store.Snapshots("blog")
	require.NoError(t, err)
	rs := make([]retention.Snapshot, len(rows))
	for i, s := range rows {
		rs[i] = retention.Snapshot{ID: s.ID, App: s.AppName, Label: s.Label, CreatedAt: s.CreatedAt, Auto: s.Auto}
	}
	_, wantPrune := retention.Apply(rs, retention.Default)
	require.NotEmpty(t, wantPrune, "the seed must exceed the retention policy")
	agent := &snapAgent{NodeAgent: m}
	m.SetNodeAgent(agent)

	m.PruneSnapshots("blog")

	var pruned []string
	for _, p := range wantPrune {
		pruned = append(pruned, p.ID)
	}
	assert.ElementsMatch(t, pruned, agent.deletes["blog"])
}
