package control

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

func TestForkSeedsSubvolumeFromSourceAndDeploys(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError) // no container yet -> Up creates one
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.AppSubvolume("blog"), 0o755))
	// The fake runner materializes the snapshot destination as an empty dir, so
	// stand in for the fork's on-disk effect: the fork's files dir exists with a
	// config, so the background deploy resolves.
	require.NoError(t, os.MkdirAll(m.AppFiles("blog2").Path(), 0o755))
	writeAppFile(t, m, "blog2", "hostit.yml", "mode: app\nrun: ./server")

	fork, err := m.Fork("blog", "blog2", "", &CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "blog2", fork.Name)

	// ONE snapshot seeds the fork: the source's whole subvolume (files, config,
	// data AND installed packages), writable, not read-only, created straight
	// into the fork's budget group (-i).
	group := fmt.Sprintf("1/%d", workspace.UIDFor(fork.Port))
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot -i "+group+" "+m.AppSubvolume("blog")+" "+m.AppSubvolume("blog2"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot -r ", "the fork seed is writable")

	// A user is created, but no demo skeleton is written (the fork keeps the source's files).
	assert.Contains(t, ops.createdUsers, "blog2")
	assert.Empty(t, ops.skeletons["blog2"], "a fork keeps the source's files, no demo skeleton")

	// The app is registered and deploys; the whole forked subvolume is chowned to
	// the new uid block (the files inside included).
	_, err = m.store.App("blog2")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return strings.Contains(r.ran(), m.UnitName("blog2"))
	}, 5*time.Second, 5*time.Millisecond, "the forked app did not deploy")
	assert.NotContains(t, r.ran(), "chown", "a fork stays root-owned like every subvolume")
}

func TestForkFromSnapshotSeedsFromThatSnapshot(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.AppSubvolume("blog"), 0o755))
	require.NoError(t, os.MkdirAll(m.AppFiles("blog2").Path(), 0o755))
	writeAppFile(t, m, "blog2", "hostit.yml", "mode: app\nrun: ./server")

	snap, err := m.TakeSnapshot("blog", "checkpoint", false)
	require.NoError(t, err)
	r.reset()
	fork, err := m.Fork("blog", "blog2", snap.ID, &CreateOptions{})
	require.NoError(t, err)

	// Seeded from the snapshot's subvolume (a whole-app snapshot), not the live
	// subvolume, and joined to the NEW app's budget group at birth (-i).
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot -i "+fmt.Sprintf("1/%d", workspace.UIDFor(fork.Port))+" "+m.SnapshotPath("blog", snap.ID)+" "+m.AppSubvolume("blog2"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot "+m.AppSubvolume("blog")+" "+m.AppSubvolume("blog2"))
}

func TestForkFromUnknownSnapshotFails(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	_, err := m.Fork("blog", "blog2", "nosuchsnap", &CreateOptions{})
	assert.ErrorIs(t, err, store.ErrSnapshotNotFound)
}

func TestForkSetsDiskQuota(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.AppSubvolume("blog"), 0o755))

	fork, err := m.Fork("blog", "blog2", "", &CreateOptions{DiskMB: 256})
	require.NoError(t, err)
	// The fork gets its own hard budget cap, not the source's and not none.
	group := fmt.Sprintf("1/%d", workspace.UIDFor(fork.Port))
	assert.Contains(t, r.ran(), "btrfs qgroup limit -e 256M "+group+" "+m.config.AppsDir)
}

func TestForkUnknownSourceFails(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	_, err := m.Fork("nope", "blog2", "", &CreateOptions{})
	assert.ErrorIs(t, err, store.ErrAppNotFound)
}

func TestForkRejectsExistingName(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog2", Port: 10001, Host: store.HostLocal}))
	_, err := m.Fork("blog", "blog2", "", &CreateOptions{})
	assert.ErrorIs(t, err, ErrAppExists)
}
