package app

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

var testIDs = IDs{UID: 1001, GID: 1001, SubUID: 165536, SubGID: 165536, SubCount: 65536}

func TestContainerCreateArgsWorkspaceMode(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "python3 -m http.server $PORT"}
	require.NoError(t, conf.Validate())
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	cmd := strings.Join(args, " ")
	assert.Contains(t, cmd, "create --name hostit-app-blog")
	assert.Contains(t, cmd, "--hostname blog")
	assert.Contains(t, cmd, "--env PORT=80")
	assert.Contains(t, cmd, "--env HOME=/home/blog")
	assert.Contains(t, cmd, "--workdir /home/blog")
	assert.Contains(t, cmd, "--publish 127.0.0.1:10000:80")
	assert.Contains(t, cmd, "--volume /srv/hostit/apps/blog:/home/blog")
	assert.Contains(t, cmd, "--volume /usr/bin/hostit:/usr/bin/hostit:ro")
	assert.Contains(t, cmd, "--volume /run/hostit:/run/hostit")
	assert.Contains(t, cmd, workspaceImageTag())
	// The agent supervises the run command as PID 1
	assert.True(t, strings.HasSuffix(cmd, workspaceImageTag()+" /usr/bin/hostit agent"), cmd)
}

func TestContainerConfigHashChanges(t *testing.T) {
	t.Parallel()
	a := &store.App{Name: "blog", Port: 10000}
	conf1 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	conf2 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	conf3 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./other"}
	conf4 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server", Env: map[string]string{"K": "v"}}
	hash1 := containerConfigHash(containerCreateArgs(conf1, a, "/h", "/s", "/b", 0, testIDs))
	hash2 := containerConfigHash(containerCreateArgs(conf2, a, "/h", "/s", "/b", 0, testIDs))
	hash3 := containerConfigHash(containerCreateArgs(conf3, a, "/h", "/s", "/b", 0, testIDs))
	hash4 := containerConfigHash(containerCreateArgs(conf4, a, "/h", "/s", "/b", 0, testIDs))
	assert.Equal(t, hash1, hash2)
	// run: changes are handled by the agent (SIGHUP), not by recreating the container,
	// so the hash must NOT depend on the run command ...
	assert.Equal(t, hash1, hash3)
	// ... but image/port changes must recreate
	assert.NotEqual(t, hash1, hash4)
}

func TestWorkspaceContainerfile(t *testing.T) {
	t.Parallel()
	// The workspace image must contain the bits that make ssh/scp/sftp/rsync work
	assert.Contains(t, workspaceContainerfile, "openssh-sftp-server")
	assert.Contains(t, workspaceContainerfile, "rsync")
	assert.Contains(t, workspaceContainerfile, "FROM docker.io/library/debian")
}

func TestContainerMountsTheSocketDirectory(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, IDs{UID: 1001, GID: 1001})
	joined := strings.Join(args, " ")

	// The daemon deletes and recreates its socket on every restart, so a mount
	// of the socket file pins an inode that is already gone: the app's CLI then
	// gets "connection refused" until its container is recreated. Mounting the
	// directory lets the container resolve the current socket by path.
	assert.Contains(t, joined, "--volume /run/hostit:/run/hostit")
	assert.NotContains(t, joined, "--volume /run/hostit/hostit.sock:/run/hostit/hostit.sock")
}

func TestContainerArgsChangeWithTheVersion(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)

	// The binary is bind-mounted as a file, so replacing it on the host leaves
	// running containers on the old inode: the app's CLI and its agent stay on
	// the previous version until the container is recreated. Making the version
	// part of the container's identity is what triggers that recreate.
	assert.Contains(t, strings.Join(args, " "), "hostit.version="+Version)
	older := append([]string{}, args...)
	for i, arg := range older {
		if arg == "hostit.version="+Version {
			older[i] = "hostit.version=v0.0.1-old"
		}
	}
	assert.NotEqual(t, containerConfigHash(args), containerConfigHash(older),
		"an upgrade must make the container stale")
}

func TestContainerHasProcessAndMemoryLimits(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	joined := strings.Join(containerCreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 512, testIDs), " ")

	// A fork bomb in one app must not take the host down with it. podman has a
	// default, but a default is the distribution's opinion, not hostit's.
	assert.Contains(t, joined, "--pids-limit "+strconv.Itoa(maxProcesses))
	assert.Contains(t, joined, "--memory 512m")

}

func TestWorkspaceImageTagFollowsItsContent(t *testing.T) {
	t.Parallel()
	// The image is built once and then found by tag. With a fixed tag, editing
	// the Containerfile -- adding vim, say -- would never rebuild it, and the
	// change would silently never reach any app.
	tag := workspaceImageTag()
	assert.True(t, strings.HasPrefix(tag, workspaceImagePrefix+":"), "got %q", tag)
	assert.Equal(t, tag, workspaceImageTag(), "the same content must give the same tag")
	assert.NotEqual(t, tag, imageTagFor("FROM scratch\n"), "different content must give a different tag")
}

func TestAppsListenOnPortEightyInside(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./bin/server"}
	a := &store.App{Name: "blog", Port: 10007}
	joined := strings.Join(containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs), " ")

	// The host port is hostit's bookkeeping. Inside its own network namespace an
	// app has :80 to itself, so that is what it should listen on -- nobody should
	// have to care which number the platform picked outside.
	assert.Contains(t, joined, fmt.Sprintf("--publish 127.0.0.1:%d:%d", a.Port, containerPort))
	assert.Contains(t, joined, fmt.Sprintf("--env PORT=%d", containerPort))
	assert.Equal(t, 80, containerPort)
}
