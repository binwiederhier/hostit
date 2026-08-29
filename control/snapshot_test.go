package control

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/control/retention"
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
	m.PushMirror()
	require.NoError(t, os.MkdirAll(m.testMachine().AppFiles("blog").Path(), 0o755))
	writeAppFile(t, m, "blog", "hostit.yml", "mode: app\nrun: ./server")

	target, err := m.testMachine().TakeSnapshot("blog", "target", false)
	require.NoError(t, err)
	require.NoError(t, m.testMachine().Rollback("blog", target.ID))

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
	assert.Contains(t, r.ran(), "systemctl enable --now "+m.testMachine().UnitName("blog"), "the app is brought back up after rollback")
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
	snap, err := m.testMachine().TakeSnapshot("blog", "save", false)
	require.NoError(t, err)
	// Created INTO the budget group (-i): atomic membership, no post-hoc assign
	// (which would leave the group unenforced until a rescan).
	assert.Contains(t, r.ran(), fmt.Sprintf("btrfs subvolume snapshot -r -i 1/%d %s %s", workspace.UIDFor(a.Port), m.testMachine().AppSubvolume("blog"), m.testMachine().SnapshotPath("blog", snap.ID)))
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

// makeOverdue leaves an app with one very old automatic snapshot and nothing
// newer, so the sweep must take another whatever its slot says. It REPLACES the
// app's rows because creating an app deploys it, and that deploy's own safety
// snapshot would otherwise be the app's most recent one -- which is exactly
// what stops it being due.
//
// The sweep tests are about what the sweep COMMANDS, not about when an app is
// due (that is snapshotsched_test.go), so they say "this app is due" out loud
// rather than depending on the wall clock landing in the right slot. The wait
// matters: that deploy runs in the background, so replacing the rows without it
// races the snapshot it is about to add.
func makeOverdue(t *testing.T, m *Manager, names ...string) {
	t.Helper()
	m.WaitBackground() // the create's deploy snapshot must land BEFORE it is replaced
	for _, name := range names {
		require.NoError(t, m.store.ReplaceAppSnapshots(name, []*store.Snapshot{{
			ID:        "overdue-" + name,
			AppName:   name,
			Label:     snapshot.AutoSnapshotLabel,
			CreatedAt: time.Now().UTC().Add(-30 * 24 * time.Hour),
			Auto:      true,
		}}))
	}
}

// The automatic snapshot is a CONTROL decision: the sweep walks the
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
	makeOverdue(t, m, "one", "two")
	agent := &snapAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, agent)

	m.autoSnapshotSweep(time.Now())

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
	makeOverdue(t, m, "one", "two")
	agent := &snapAgent{NodeAgent: m.testMachine(), failFor: "one"}
	m.NodeRegistry().Register(store.HostLocal, agent)

	m.autoSnapshotSweep(time.Now())

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
	agent := &snapAgent{NodeAgent: m.testMachine()}
	m.NodeRegistry().Register(store.HostLocal, agent)

	m.PruneSnapshots("blog")

	var pruned []string
	for _, p := range wantPrune {
		pruned = append(pruned, p.ID)
	}
	assert.ElementsMatch(t, pruned, agent.deletes["blog"])
}

// snapshotReporter is a node agent that reports its own snapshot records, the
// way a reconnecting node does.
type snapshotReporter struct {
	NodeAgent
	report []*store.Snapshot
}

func (a *snapshotReporter) Snapshots() ([]*store.Snapshot, error) { return a.report, nil }

// A snapshot that completed just as the connection dropped never delivered its
// SnapshotsChanged callback, and the rejoin mirror push would then overwrite
// the node's rows -- losing the record while the subvolume stays (invisible to
// retention, still charged to the app's budget). On rejoin control therefore
// ingests the node's own records BEFORE pushing the mirror back.
func TestIngestNodeSnapshotsRecoversRecordsMissedWhileDisconnected(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	// An app hosted on the remote node (create places locally in tests, so the
	// row is written directly with that node as its host).
	require.NoError(t, m.store.AddApp(&store.App{ID: store.NewAppID(), Name: "blog", Port: 10500, Host: "worker-2"}))
	m.PushMirror()
	agent := &snapshotReporter{NodeAgent: m.testMachine(), report: []*store.Snapshot{
		{ID: "20260816-120000-lost", AppName: "blog", Label: "taken during the outage", CreatedAt: time.Now().UTC().Truncate(time.Second), Auto: true},
	}}

	m.IngestNodeSnapshots("worker-2", agent)

	snaps, err := m.ListSnapshots("blog")
	require.NoError(t, err)
	var ids []string
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	assert.Contains(t, ids, "20260816-120000-lost", "the record the node kept is recovered")
}

// A node may only report snapshots for the apps it actually hosts: the same
// scoping the reverse-channel callbacks enforce, so a compromised node cannot
// rewrite another node's app records.
func TestIngestNodeSnapshotsRejectsForeignApps(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t)
	_, err := m.CreateApp("mine", nil) // stays on the local node
	require.NoError(t, err)
	before, err := m.ListSnapshots("mine")
	require.NoError(t, err)
	agent := &snapshotReporter{NodeAgent: m.testMachine(), report: []*store.Snapshot{
		{ID: "20260816-120000-evil", AppName: "mine", Label: "not yours", CreatedAt: time.Now().UTC()},
	}}

	m.IngestNodeSnapshots("worker-2", agent)

	after, err := m.ListSnapshots("mine")
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "a node cannot rewrite another node's app snapshots")
}
