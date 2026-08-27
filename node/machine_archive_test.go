package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/ssh"
	"heckel.io/hostit/system/systemd"
)

// recordRunner is a nop runner that records every "subvolume delete" target, so
// a test can assert exactly which subvolumes the sweep tried to remove.
type recordRunner struct {
	run.Nop
	deleted []string
}

func (r *recordRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	if len(args) >= 4 && args[0] == "btrfs" && args[1] == "subvolume" && args[2] == "delete" {
		r.deleted = append(r.deleted, args[3])
	}
	return "", nil
}

func newSweepTestMachine(t *testing.T, rec *recordRunner) *Machine {
	t.Helper()
	conf := NewConfig()
	conf.DataDir, conf.AppsDir = t.TempDir(), t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewMachine(conf, s, &Services{
		Runner: run.Nop{}, Btrfs: btrfs.New(rec), Systemd: systemd.New(run.Nop{}),
		Container: podman.New(run.Nop{}), User: nopUser{}, SSH: ssh.New(), Firewall: nopFirewall{},
	})
}

func TestSweepExportSnapshotsRemovesOnlyStaleOnes(t *testing.T) {
	t.Parallel()
	rec := &recordRunner{}
	m := newSweepTestMachine(t, rec)

	// A stale export snapshot (mtime well past the max age), a fresh one (an
	// export in flight), and an ordinary app subvolume that must never be touched.
	stale := filepath.Join(m.config.AppsDir, exportSnapPrefix+"stale")
	fresh := filepath.Join(m.config.AppsDir, exportSnapPrefix+"fresh")
	app := filepath.Join(m.config.AppsDir, "myapp")
	for _, d := range []string{stale, fresh, app} {
		require.NoError(t, os.Mkdir(d, 0755))
	}
	old := time.Now().Add(-2 * exportSnapMaxAge)
	require.NoError(t, os.Chtimes(stale, old, old))

	n := m.SweepExportSnapshots()

	assert.Equal(t, 1, n)
	assert.Equal(t, []string{stale}, rec.deleted, "only the stale export snapshot may be swept")
}

func TestArchiveSnapshotRejectsUnknownID(t *testing.T) {
	t.Parallel()
	m := newSweepTestMachine(t, &recordRunner{})

	// A crafted traversal id is not in the store, so it is refused before any
	// path is built -- never an archive of something outside the app's snapshots.
	rc, err := m.ArchiveSnapshot("app", "../../etc", archive.Zip)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrSnapshotNotFound)
	assert.Nil(t, rc)
}

func TestArchiveSnapshotRejectsCrossApp(t *testing.T) {
	t.Parallel()
	m := newSweepTestMachine(t, &recordRunner{})
	require.NoError(t, m.store.AddSnapshot(&store.Snapshot{ID: "snap1", AppName: "other", CreatedAt: time.Now()}))

	// The snapshot exists, but it belongs to another app: one app must not export
	// another's snapshot by guessing its id.
	rc, err := m.ArchiveSnapshot("app", "snap1", archive.Zip)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrSnapshotNotFound)
	assert.Nil(t, rc)
}
