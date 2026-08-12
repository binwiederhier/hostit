package btrfs

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeRunner records the commands it is asked to run and can be primed to return
// canned output for a command whose joined args start with a given prefix.
type fakeRunner struct {
	ran     []string
	outputs map[string]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{outputs: map[string]string{}}
}

func (f *fakeRunner) returns(prefix, out string) { f.outputs[prefix] = out }

func (f *fakeRunner) record(args []string) (string, error) {
	joined := strings.Join(args, " ")
	f.ran = append(f.ran, joined)
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

func TestUsageMB(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.returns("btrfs qgroup show", `Qgroupid  Referenced  Exclusive  Path
--------  ----------  ---------  ----
0/258     268435456   268435456  blog
`)
	assert.Equal(t, 256, New(r).UsageMB("/apps/blog"))
}
