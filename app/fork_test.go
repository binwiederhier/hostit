package app

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestForkSeedsHomeFromSourceAndDeploys(t *testing.T) {
	t.Parallel()
	m, ops, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError) // no container yet -> Up creates one
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	require.NoError(t, os.MkdirAll(m.workspace.RootfsPath(m.appID("blog")), 0o700))
	// The fake runner never touches disk, so stand in for the snapshot's on-disk
	// effect: the forked home exists with the source's files, so the deploy resolves.
	require.NoError(t, os.MkdirAll(m.appHome("blog2"), 0o755))
	writeAppFile(t, m, "blog2", "hostit.yml", "mode: app\nrun: ./server")

	fork, err := m.Fork("blog", "blog2", "", &CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "blog2", fork.Name)

	// The home is seeded from a WRITABLE snapshot of the source home, not read-only.
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+m.appHome("blog")+" "+m.appHome("blog2"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot -r "+m.appHome("blog")+" "+m.appHome("blog2"))

	// The rootfs is forked from the SOURCE's rootfs (installed packages carry
	// over), not snapshotted fresh from the base.
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+m.workspace.RootfsPath(m.appID("blog"))+" "+m.workspace.RootfsPath(fork.ID))

	// A user is created, but no demo skeleton is written (the fork keeps the source's files).
	assert.Contains(t, ops.createdUsers, "blog2")
	assert.Empty(t, ops.skeletons["blog2"], "a fork keeps the source's files, no demo skeleton")

	// The app is registered and deploys; its home is chowned to the new uid.
	_, err = m.store.App("blog2")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return strings.Contains(r.ran(), m.unitName("blog2"))
	}, 5*time.Second, 5*time.Millisecond, "the forked app did not deploy")
	uid := m.uidFor(fork.Port)
	assert.Contains(t, r.ran(), fmt.Sprintf("chown -R %d:%d %s", uid, uid, m.appHome("blog2")))
}

func TestForkFromSnapshotSeedsFromThatSnapshot(t *testing.T) {
	t.Parallel()
	m, _, r := newTestDeployManager(t)
	r.returns("stat -f", "btrfs\n")
	r.failOn("container inspect", assert.AnError)
	require.NoError(t, m.store.AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal}))
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	require.NoError(t, os.MkdirAll(m.workspace.RootfsPath(m.appID("blog")), 0o700))
	require.NoError(t, os.MkdirAll(m.appHome("blog2"), 0o755))
	writeAppFile(t, m, "blog2", "hostit.yml", "mode: app\nrun: ./server")

	snap, err := m.TakeSnapshot("blog", "checkpoint", false)
	require.NoError(t, err)
	r.reset()
	_, err = m.Fork("blog", "blog2", snap.ID, &CreateOptions{})
	require.NoError(t, err)

	// Seeded from the snapshot's subvolume, not the live home.
	assert.Contains(t, r.ran(), "btrfs subvolume snapshot "+m.snapshotPath("blog", snap.ID)+" "+m.appHome("blog2"))
	assert.NotContains(t, r.ran(), "btrfs subvolume snapshot "+m.appHome("blog")+" "+m.appHome("blog2"))
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
	require.NoError(t, os.MkdirAll(m.appHome("blog"), 0o755))
	require.NoError(t, os.MkdirAll(m.workspace.RootfsPath(m.appID("blog")), 0o700))

	fork, err := m.Fork("blog", "blog2", "", &CreateOptions{DiskMB: 256})
	require.NoError(t, err)
	// The fork gets its own hard budget cap, not the source's and not none.
	group := fmt.Sprintf("1/%d", m.uidFor(fork.Port))
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
