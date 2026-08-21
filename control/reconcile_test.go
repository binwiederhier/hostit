package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
	"heckel.io/hostit/unixuser"
)

// An app deleted while the daemon was down leaves its subvolume behind;
// reconcile reaps it like orphan units and containers. A live app's subvolume
// is never touched (the never-recreated invariant would make that data loss),
// and neither are hidden entries (.bases, .snapshots, dotfiles).
func TestReconcileOrphansRemovesOrphanSubvolume(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.testMachine().WriteFile("blog", "x", []byte("y"), 0)) // a live app: its subvolume exists on disk
	orphan := filepath.Join(m.config.AppsDir, "ghostid")
	require.NoError(t, os.MkdirAll(orphan, 0o700))
	hidden := filepath.Join(m.config.AppsDir, ".snapshots")
	require.NoError(t, os.MkdirAll(hidden, 0o755))
	r.reset()

	m.testMachine().ReconcileOrphans() // first sighting: a removal always needs a second
	removed := m.testMachine().ReconcileOrphans()
	assert.Contains(t, removed, "ghostid")
	assert.Contains(t, r.ran(), "btrfs subvolume delete "+orphan)
	assert.NoDirExists(t, orphan)
	assert.NotContains(t, r.ran(), "btrfs subvolume delete "+m.testMachine().AppSubvolume("blog"), "a live app's subvolume must be left alone")
	assert.DirExists(t, m.testMachine().AppSubvolume("blog"))
	assert.DirExists(t, hidden, "hidden entries are never touched")
}

// The userdel stub -- a root-owned plain directory left where the subvolume was
// -- is not a subvolume, so `btrfs subvolume delete` refuses it; the sweep
// removes it directly when empty and surfaces (rather than force-deletes) a
// path that still will not go away.
func TestReconcileOrphansRemovesEmptyStubKeepsStubborn(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	stub := filepath.Join(m.config.AppsDir, "stubid")
	require.NoError(t, os.MkdirAll(stub, 0o755))
	stubborn := filepath.Join(m.config.AppsDir, "stubbornid")
	require.NoError(t, os.MkdirAll(stubborn, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stubborn, "f"), []byte("d"), 0o644))
	// The fake btrfs "deletes" without touching disk, standing in for the real
	// tool refusing a plain directory; only the empty stub is then removable.
	r.reset()

	m.testMachine().ReconcileOrphans() // first sighting: a removal always needs a second
	removed := m.testMachine().ReconcileOrphans()
	assert.Contains(t, removed, "stubid")
	assert.NoDirExists(t, stub)
	assert.NotContains(t, removed, "stubbornid")
	assert.DirExists(t, stubborn, "a path that will not go away is surfaced, not force-deleted")
}

// A deleted app can leave a file-less directory tree behind (<id>/home/app,
// recreated by a state or file read racing the delete -- see TODO.md). Holding
// no files, clearing it cannot lose anything, so the sweep removes it instead
// of warning on every start; a tree with any file in it is still surfaced.
func TestReconcileOrphansRemovesFileLessStubTree(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	stub := filepath.Join(m.config.AppsDir, "stubtree")
	require.NoError(t, os.MkdirAll(filepath.Join(stub, "home", "app"), 0o750))
	withFile := filepath.Join(m.config.AppsDir, "keeptree")
	require.NoError(t, os.MkdirAll(filepath.Join(withFile, "home", "app"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(withFile, "home", "app", "data"), []byte("x"), 0o644))
	r.reset()

	m.testMachine().ReconcileOrphans() // first sighting: a removal always needs a second
	removed := m.testMachine().ReconcileOrphans()
	assert.Contains(t, removed, "stubtree")
	assert.NoDirExists(t, stub)
	assert.NotContains(t, removed, "keeptree")
	assert.DirExists(t, withFile, "a tree holding files is surfaced, never force-deleted")
}

func TestReconcileOrphansSweepsStrayBudgetGroups(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	createTestApp(t, m, "blog") // port 10000 -> uid 1000000
	// A destroy that stayed "busy" during app delete leaves the budget group
	// behind; the reconcile sweeps any 1/<uid> group whose uid maps to no app.
	runner.returns("btrfs qgroup show", "0/5 16384 16384\n1/1000000 100 100\n1/1065536 100 100\n")
	runner.reset()
	m.testMachine().ReconcileOrphans() // first sighting: nothing is removed yet
	m.testMachine().ReconcileOrphans()
	ran := runner.ran()
	assert.Contains(t, ran, "btrfs qgroup destroy 1/1065536", "the stray group (no app on that uid) is destroyed")
	assert.NotContains(t, ran, "btrfs qgroup destroy 1/1000000", "the live app's group must survive")
}

// Colocated nodes share one /etc/passwd, so the sweep must only ever touch
// accounts whose home lies under THIS node's apps pool. Deleting another
// node's app accounts would take its apps down.
func TestReconcileOrphansLeavesOtherNodesAccountsAlone(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	ops.accounts = []unixuser.Account{
		{Name: "neighbor", Home: "/var/lib/hostit-node2/apps/othernode99/home/app"},
		{Name: "stray", Home: "/home/somebody"},
	}

	m.testMachine().ReconcileOrphans() // first sighting: nothing is removed yet
	m.testMachine().ReconcileOrphans()

	assert.Empty(t, ops.deletedUsers, "accounts outside this node's pool are none of its business")
}

// The orphan-account sweep must never delete an app that simply is not in
// this mirror YET. A create provisions the account and the registry push
// follows, so a reconcile landing in that window would see a live app as an
// orphan -- which is exactly what happened on stage: an app was created and
// its unix account was deleted two seconds later, so it never served.
//
// An account is therefore only removed if it was already orphaned in the
// PREVIOUS sweep: a race resolves itself because the next mirror carries the
// app, while a genuine leftover is still absent and goes.
func TestReconcileOrphanUsersNeedsTwoSightings(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	ops.accounts = []unixuser.Account{
		{Name: "blog", Home: m.testMachine().AppFiles("blog").Path()},
		{Name: "newcomer", Home: filepath.Join(m.config.AppsDir, "id-not-yet-mirrored", "home", "app")},
	}

	m.testMachine().ReconcileOrphans()
	assert.Empty(t, ops.deletedUsers, "a first sighting is never enough: the app may just be mid-create")

	// The app arrived in the mirror before the next sweep: never touched.
	require.NoError(t, m.store.AddApp(&store.App{ID: "id-not-yet-mirrored", Name: "newcomer", Port: 10777, Host: store.HostLocal}))
	m.PushMirror()
	m.testMachine().ReconcileOrphans()
	assert.Empty(t, ops.deletedUsers, "an account that showed up in the mirror is not an orphan")
}

// A genuine leftover -- absent from the mirror across two sweeps -- is removed.
func TestReconcileOrphanUsersRemovesAPersistentOrphan(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	ops.accounts = []unixuser.Account{
		{Name: "blog", Home: m.testMachine().AppFiles("blog").Path()},
		{Name: "ghost", Home: filepath.Join(m.config.AppsDir, "deadbeef1234", "home", "app")},
	}

	m.testMachine().ReconcileOrphans()
	m.testMachine().ReconcileOrphans()

	assert.Equal(t, []string{"ghost"}, ops.deletedUsers)
	assert.Contains(t, ops.killedUsers, "ghost")
}

// Control asserts a whole desired document and the node converges to it: an
// app whose account is missing (a rebuilt node, a botched provision, an
// account swept by mistake) is built again from the same spec that created it
// originally, and its keys and limits are re-applied. This is what makes the
// registry the source of truth -- a node keeps no authority of its own, so
// any component can crash and the next reconcile repairs it.
func TestReconcileBuildsAnAppMissingFromTheNode(t *testing.T) {
	t.Parallel()
	m, ops, _ := newTestDeployManager(t)
	app := createTestApp(t, m, "blog")
	desired, err := m.DesiredState("")
	require.NoError(t, err)
	require.Len(t, desired.Apps, 1)

	// The account is gone, as it would be on a rebuilt node.
	ops.existingUsers, ops.createdUsers = nil, nil
	require.NoError(t, m.store.SetAppKeys("blog", []string{testPublicKey}))
	desired, err = m.DesiredState("")
	require.NoError(t, err)

	m.testMachine().Reconcile(desired)

	assert.Contains(t, ops.createdUsers, "blog", "an app the document lists but the node lacks is provisioned")
	assert.Contains(t, ops.authorizedKeys["blog"], testPublicKey, "its keys are asserted from the registry")
	_ = app
}

// The document is the truth for the sweep too: an app control no longer lists
// is torn down (after the confirming second pass), while everything in the
// document is left alone.
func TestReconcileSweepsWhatTheDocumentOmits(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.testMachine().WriteFile("blog", "x", []byte("y"), 0))
	desired, err := m.DesiredState("")
	require.NoError(t, err)

	// A subvolume of an app the document does not list.
	require.NoError(t, os.MkdirAll(filepath.Join(m.config.AppsDir, "ghostid"), 0o755))
	r.reset()
	m.testMachine().Reconcile(desired)
	m.testMachine().Reconcile(desired)

	assert.Contains(t, r.ran(), "btrfs subvolume delete "+filepath.Join(m.config.AppsDir, "ghostid"))
	assert.DirExists(t, m.testMachine().AppSubvolume("blog"), "an app the document lists is never touched")
}

// A poweroff is an operator's decision, and reconcile must not undo it. The
// app is still rebuilt when the node lacks it -- the account and its subvolume
// should exist, so data can be restored and the app powered on later -- but it
// must not come back RUNNING. Before this, a wiped node (or an account swept
// by mistake) quietly resurrected every app an operator had deliberately
// stopped.
func TestReconcileDoesNotResurrectAPoweredOffApp(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	createTestApp(t, m, "blog")
	require.NoError(t, m.store.SetAppPoweredOff("blog", true))
	m.PushMirror()

	// The account is gone, as it would be on a rebuilt node.
	ops.existingUsers, ops.createdUsers = nil, nil
	desired, err := m.DesiredState("")
	require.NoError(t, err)
	require.Len(t, desired.Apps, 1)
	require.True(t, desired.Apps[0].PoweredOff, "control carries the poweroff in the desired state")
	r.reset()

	m.testMachine().Reconcile(desired)

	assert.Contains(t, ops.createdUsers, "blog", "the app is still rebuilt: it exists, it just does not run")
	assert.Contains(t, r.ran(), "disable --now "+m.testMachine().UnitName("blog"), "and is left powered off")
	a, err := m.store.App("blog")
	require.NoError(t, err)
	assert.True(t, a.PoweredOff, "the recorded intent survives the pass")
}
