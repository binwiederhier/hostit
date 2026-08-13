package btrfs

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestSetQuota(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	s := New(r)

	assert.NoError(t, s.SetQuota("/apps/blog", 512))
	assert.Contains(t, r.ran, "btrfs qgroup limit 512M /apps/blog")

	r.ran = nil
	assert.NoError(t, s.SetQuota("/apps/blog", 0))
	assert.Contains(t, r.ran, "btrfs qgroup limit none /apps/blog")
}

func TestParseQgroupReferencedMB(t *testing.T) {
	t.Parallel()
	out := `Qgroupid         Referenced    Exclusive  Path
--------         ----------    ---------  ----
0/257            134217728     134217728  blog
`
	assert.Equal(t, 128, parseQgroupReferencedMB(out)) // 134217728 bytes = 128 MB
	assert.Equal(t, 0, parseQgroupReferencedMB(""))
	assert.Equal(t, 0, parseQgroupReferencedMB("garbage\nno rows here"))
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

func TestUsageMB(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("btrfs qgroup show", `Qgroupid  Referenced  Exclusive  Path
--------  ----------  ---------  ----
0/258     268435456   268435456  blog
`)
	assert.Equal(t, 256, New(r).UsageMB("/apps/blog"))
}
