package btrfs

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner records the commands it is asked to run and can be primed to return
// canned output (or an error) for a command whose joined args start with a given
// prefix.
type fakeRunner struct {
	ran     []string
	outputs map[string]string
	errs    map[string]error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{outputs: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeRunner) returns(prefix, out string) { f.outputs[prefix] = out }

func (f *fakeRunner) fails(prefix string, err error) { f.errs[prefix] = err }

func (f *fakeRunner) record(args []string) (string, error) {
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, joined)
	for prefix, err := range f.errs {
		if strings.HasPrefix(joined, prefix) {
			return f.outputs[prefix], err
		}
	}
	for prefix, out := range f.outputs {
		if strings.HasPrefix(joined, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) Run(args ...string) (string, error) { return f.record(args) }

func (f *fakeRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	return f.record(args)
}

func TestFilesystemDetection(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("stat -f", "btrfs\n")
	s := New(r)
	assert.True(t, s.IsBtrfs("/apps"))

	off := newFakeRunner()
	off.returns("stat -f", "ext2/ext3\n")
	assert.False(t, New(off).IsBtrfs("/apps"))

	// Unreadable (default fake output is empty) means not btrfs, so hostit keeps
	// the plain-directory behavior.
	assert.False(t, New(newFakeRunner()).IsBtrfs("/apps"))
}

func TestSubvolumeCommands(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	assert.NoError(t, s.CreateSubvolume("/apps/blog"))
	assert.Contains(t, r.ran, "btrfs subvolume create /apps/blog")

	r.ran = nil
	assert.NoError(t, s.Snapshot("/apps/blog", "/apps/.snapshots/blog/x", true))
	assert.Contains(t, r.ran, "btrfs subvolume snapshot -r /apps/blog /apps/.snapshots/blog/x")

	r.ran = nil
	assert.NoError(t, s.Snapshot("/apps/.snapshots/blog/x", "/apps/blog", false))
	assert.Contains(t, r.ran, "btrfs subvolume snapshot /apps/.snapshots/blog/x /apps/blog")
	for _, cmd := range r.ran {
		assert.NotContains(t, cmd, "-r", "a rollback/fork copy is writable")
	}

	r.ran = nil
	assert.NoError(t, s.MoveSubvolume("/apps/staged", "/apps/blog"))
	assert.Contains(t, r.ran, "mv /apps/staged /apps/blog")

	r.ran = nil
	assert.NoError(t, s.DeleteSubvolume("/apps/blog"))
	assert.Contains(t, r.ran, "btrfs subvolume delete /apps/blog")
}

func TestSetReadOnly(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	assert.NoError(t, s.SetReadOnly("/apps/.bases/abc", true))
	assert.Contains(t, r.ran, "btrfs property set /apps/.bases/abc ro true")

	r.ran = nil
	assert.NoError(t, s.SetReadOnly("/apps/.bases/abc", false))
	assert.Contains(t, r.ran, "btrfs property set /apps/.bases/abc ro false")
}

func TestQuotaEnable(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	assert.NoError(t, New(r).QuotaEnable("/apps"))
	assert.Contains(t, r.ran, "btrfs quota enable /apps")
}

func TestRootID(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("btrfs inspect-internal rootid", "257\n")
	id, err := New(r).RootID("/apps/blog")
	assert.NoError(t, err)
	assert.Equal(t, "257", id)
	assert.Contains(t, r.ran, "btrfs inspect-internal rootid /apps/blog")
}

func TestQgroupCommands(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	assert.NoError(t, s.QgroupCreate("/apps", "1/1000000"))
	assert.Contains(t, r.ran, "btrfs qgroup create 1/1000000 /apps")

	r.ran = nil
	assert.NoError(t, s.QgroupAssign("/apps", "0/257", "1/1000000"))
	assert.Contains(t, r.ran, "btrfs qgroup assign 0/257 1/1000000 /apps")

	r.ran = nil
	assert.NoError(t, s.QgroupDestroy("/apps", "1/1000000"))
	assert.Contains(t, r.ran, "btrfs qgroup destroy 1/1000000 /apps")
}

func TestQgroupAssignToleratesRescanWarning(t *testing.T) {
	t.Parallel()
	// btrfs qgroup assign exits non-zero when it merely recommends a quota rescan;
	// that is bookkeeping advice, not a failure, and must not fail the assign.
	r := newFakeRunner()
	r.returns("btrfs qgroup assign", "WARNING: quotas may be inconsistent, rescan needed\n")
	r.fails("btrfs qgroup assign", assert.AnError)
	assert.NoError(t, New(r).QgroupAssign("/apps", "0/257", "1/1000000"))

	// A real failure (no rescan hint) still surfaces.
	hard := newFakeRunner()
	hard.fails("btrfs qgroup assign", assert.AnError)
	assert.Error(t, New(hard).QgroupAssign("/apps", "0/257", "1/1000000"))
}

func TestQgroupAssignIsIdempotent(t *testing.T) {
	t.Parallel()
	// Assigning a subvolume that is already a member errors with "File exists".
	// Every budget path is ensure-style and re-runs after partial failures (the
	// storage migration retries whole apps), so an existing membership is success,
	// not an error -- without this the migration can never converge.
	r := newFakeRunner()
	r.returns("btrfs qgroup assign", "ERROR: unable to assign quota group: File exists\n")
	r.fails("btrfs qgroup assign", assert.AnError)
	assert.NoError(t, New(r).QgroupAssign("/apps", "0/257", "1/1000000"))
}

func TestQgroupLimitExclusive(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	// The cap must be on exclusive bytes: a referenced limit would count the base
	// subvolume's shared extents and wedge the container immediately.
	assert.NoError(t, s.QgroupLimitExclusive("/apps", "1/1000000", 2048))
	assert.Contains(t, r.ran, "btrfs qgroup limit -e 2048M 1/1000000 /apps")

	r.ran = nil
	assert.NoError(t, s.QgroupLimitExclusive("/apps", "1/1000000", 0))
	assert.Contains(t, r.ran, "btrfs qgroup limit -e none 1/1000000 /apps")
}

func TestExclusiveUsageMB(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("btrfs qgroup show", `Qgroupid         Referenced    Exclusive  Path
--------         ----------    ---------  ----
0/5              16384         16384      <toplevel>
0/257            904921088     49152      blog
1/1000000        904921088     52428800   <2 member qgroups>
`)
	assert.Equal(t, 50, New(r).ExclusiveUsageMB("/apps", "1/1000000")) // 52428800 bytes = 50 MB
	assert.Contains(t, r.ran, "btrfs qgroup show --raw /apps")
	// Missing group or unreadable output degrades to 0 rather than failing.
	assert.Equal(t, 0, New(r).ExclusiveUsageMB("/apps", "1/9999999"))
	assert.Equal(t, 0, New(newFakeRunner()).ExclusiveUsageMB("/apps", "1/1000000"))
}

func TestQgroupDestroyRetriesAfterSyncWhenBusy(t *testing.T) {
	t.Parallel()
	// Destroying a group right after deleting its member subvolumes fails with
	// "Device or resource busy" until the btrfs transaction commits. A filesystem
	// sync forces that commit, so one sync+retry turns the common app-delete case
	// from a warning + leftover group into a clean destroy.
	r := newFakeRunner()
	r.returns("btrfs qgroup destroy", "ERROR: unable to destroy quota group: Device or resource busy\n")
	r.fails("btrfs qgroup destroy", assert.AnError)
	err := New(r).QgroupDestroy("/apps", "1/1000000")
	assert.Error(t, err, "still busy after the whole ladder surfaces the error")
	joined := strings.Join(r.ran, "\n")
	assert.Contains(t, joined, "btrfs filesystem sync /apps")
	// The full ladder: destroy -> sync + destroy -> remove members + destroy.
	assert.Equal(t, 3, strings.Count(joined, "btrfs qgroup destroy 1/1000000 /apps"))
}

func TestListBudgetGroups(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("btrfs qgroup show", `qgroupid         rfer         excl
--------         ----         ----
0/5           16.00KiB     16.00KiB
0/412        455.17MiB     16.52MiB
1/1000000    480.84MiB     42.19MiB
1/1131072    794.59MiB     60.71MiB
`)
	groups, err := New(r).ListBudgetGroups("/apps")
	require.NoError(t, err)
	assert.Equal(t, []string{"1/1000000", "1/1131072"}, groups)
}

func TestQgroupDestroyRemovesStaleMembersWhenStillBusy(t *testing.T) {
	t.Parallel()
	// A group can stay "busy" forever: members whose subvolumes are long deleted
	// remain assigned as stale 0/<id> qgroups, and btrfs refuses to destroy a
	// group that has members. When sync+retry does not clear it, the members must
	// be removed from the group first (seen live: a leftover group with six
	// <stale> members survived every destroy).
	r := newFakeRunner()
	r.returns("btrfs qgroup destroy", "ERROR: unable to destroy quota group: Device or resource busy\n")
	r.fails("btrfs qgroup destroy", assert.AnError)
	// Real -pc output has a trailing path column ("<0 member qgroups>" for a
	// level-1 group, with spaces!), so the child list is NOT the last field --
	// grabbing the last field fed "qgroups>" to qgroup remove on stage.
	r.returns("btrfs qgroup show -pc", `qgroupid  rfer   excl   parent     child          path
0/1612    0      0      1/1262144  -              <stale>
0/1614    0      0      1/1262144  -              <stale>
1/1262144 0      0      -          0/1612,0/1614  <0 member qgroups>
`)
	err := New(r).QgroupDestroy("/apps", "1/1262144")
	assert.Error(t, err, "the fake keeps failing the destroy; the removals still must have run")
	joined := strings.Join(r.ran, "\n")
	assert.Contains(t, joined, "btrfs qgroup remove 0/1612 1/1262144 /apps")
	assert.Contains(t, joined, "btrfs qgroup remove 0/1614 1/1262144 /apps")
	// destroy attempted: initial, post-sync, post-removal
	assert.Equal(t, 3, strings.Count(joined, "btrfs qgroup destroy 1/1262144 /apps"))
}

func TestQgroupTryDestroyNeverSyncs(t *testing.T) {
	t.Parallel()
	// The gentle single attempt for background teardowns: a filesystem sync
	// forces a full transaction commit and stalls every concurrent btrfs
	// operation on the pool (an app create's snapshot waited ~12s behind one),
	// so the teardown path polls this instead and leaves the heavy ladder to
	// the startup reconcile.
	r := newFakeRunner()
	r.returns("btrfs qgroup destroy", "ERROR: unable to destroy quota group: Device or resource busy\n")
	r.fails("btrfs qgroup destroy", assert.AnError)
	err := New(r).QgroupTryDestroy("/apps", "1/1000000")
	assert.Error(t, err, "busy surfaces so the caller can poll")
	joined := strings.Join(r.ran, "\n")
	assert.Contains(t, joined, "btrfs qgroup destroy 1/1000000 /apps")
	assert.NotContains(t, joined, "filesystem sync", "the gentle path must never force a commit")
	assert.NotContains(t, joined, "qgroup remove")
	assert.Equal(t, 1, strings.Count(joined, "btrfs qgroup destroy"), "one attempt, no ladder")
}
