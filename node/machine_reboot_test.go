package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/container"
	"heckel.io/hostit/ssh"
	"heckel.io/hostit/store"
	"heckel.io/hostit/systemd"
	"heckel.io/hostit/workspace"
)

// scriptedRunner records every command and answers "podman ... inspect" with a
// fixed value, so a test can steer the desired-vs-running config comparison.
type scriptedRunner struct {
	calls       [][]string
	inspectSays string
}

func (r *scriptedRunner) Run(args ...string) (string, error) {
	r.calls = append(r.calls, args)
	for _, a := range args {
		if a == "inspect" {
			return r.inspectSays, nil
		}
	}
	return "", nil
}

func (r *scriptedRunner) RunTimeout(_ time.Duration, args ...string) (string, error) {
	return r.Run(args...)
}

func (r *scriptedRunner) sawCreateWith(flag string) bool {
	for _, call := range r.calls {
		if len(call) > 1 && call[0] == "podman" && call[1] == "create" {
			if strings.Contains(strings.Join(call, " "), flag) {
				return true
			}
		}
	}
	return false
}

func (r *scriptedRunner) saw(first, second string) bool {
	for _, call := range r.calls {
		if len(call) > 1 && call[0] == first && call[1] == second {
			return true
		}
	}
	return false
}

// A reboot picks up pending container config: recorded memory/CPU limits are
// applied by RECREATING the container when the desired args differ from the
// running ones -- that is what "applies at the next reboot" promises. When
// nothing is pending, a reboot stays the plain unit restart it always was.
func TestRebootConvergesPendingLimits(t *testing.T) {
	t.Parallel()
	conf := NewConfig()
	conf.DataDir, conf.AppsDir = t.TempDir(), t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	runner := &scriptedRunner{inspectSays: "some-other-hash"}
	m := NewMachine(conf, s, &Services{
		Runner: runner, Btrfs: btrfs.New(runner), Systemd: systemd.New(runner),
		Container: container.New(runner), User: nopUser{}, SSH: ssh.New(), Firewall: nopFirewall{},
	})
	require.NoError(t, s.ReplaceNodeMirror([]*store.App{
		{ID: "a1", Name: "blog", Port: 10000, Host: store.HostLocal, UID: 1001, CreatedAt: time.Now()},
	}, nil))
	// The subvolume "exists" so apply's ensure short-circuits: this test is
	// about the container config diff, not the storage machinery.
	require.NoError(t, os.MkdirAll(m.AppSubvolume("blog"), 0o755))
	m.SetMemoryLimit("blog", 256)
	m.SetCPULimit("blog", 500)

	// The running container's config hash differs from the desired one, so the
	// reboot must recreate with the new caps.
	require.NoError(t, m.Restart("blog"))
	assert.True(t, runner.sawCreateWith("--memory 256m"), "the recreate carries the pending memory cap")
	assert.True(t, runner.sawCreateWith("--cpus 0.50"), "and the pending cpu cap")

	// Nothing pending: the reboot is a plain unit restart, no recreate.
	a, err := s.App("blog")
	require.NoError(t, err)
	ids := workspace.IDs{UID: 1001, GID: 1001, Count: 65536}
	desired := workspace.CreateArgs(nil, a, m.AppSubvolume("blog"), conf.SocketFile, workspace.HostBinFile, Version, 256, 500, ids, "")
	runner.calls, runner.inspectSays = nil, workspace.ConfigHash(desired)
	require.NoError(t, m.Restart("blog"))
	assert.True(t, runner.saw("systemctl", "restart"), "an up-to-date container just bounces")
	assert.False(t, runner.sawCreateWith("--memory"), "no recreate when nothing is pending")
}
