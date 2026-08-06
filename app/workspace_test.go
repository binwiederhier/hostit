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

var testIDs = IDs{UID: 1001, GID: 1001, Count: 65536}

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
	// A single contiguous id block, container 0 -> host 1001, so podman idmaps the
	// image (no per-app copy) rather than chowning it. Not the old split map.
	assert.Contains(t, cmd, "--uidmap 0:1001:65536")
	assert.Contains(t, cmd, "--gidmap 0:1001:65536")
	assert.NotContains(t, cmd, "--uidmap 0:1001:1")
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
	// Kept: the flagship Go toolchain, python for quick apps, and sqlite3 so an
	// owner can inspect a persistent app's database over SSH
	assert.Contains(t, workspaceContainerfile, "golang-go")
	assert.Contains(t, workspaceContainerfile, "python3")
	assert.Contains(t, workspaceContainerfile, "sqlite3")
	// Shell niceties (ll/colors) written into a system-wide profile.d script
	assert.Contains(t, workspaceContainerfile, "/etc/profile.d/hostit.sh")
	assert.Contains(t, workspaceContainerfile, "base64 -d")
	// Dropped to keep the image small (they inflate every per-app container and
	// the disk); an app that needs them installs them with apt-get
	assert.NotContains(t, workspaceContainerfile, "nodejs")
	assert.NotContains(t, workspaceContainerfile, "php")
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
	// Read-only: the app only connects to the socket, it never writes the dir
	assert.Contains(t, joined, "--volume /run/hostit:/run/hostit:ro")
	assert.NotContains(t, joined, "--volume /run/hostit/hostit.sock:/run/hostit/hostit.sock")
}

func TestEnvOrderDoesNotChangeTheConfigHash(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server", Env: map[string]string{
		"A": "1", "B": "2", "C": "3", "D": "4", "E": "5",
	}}
	a := &store.App{Name: "blog", Port: 10000}
	// Go randomizes map iteration per run, so without sortedKeys the env would land
	// in a different order each time and every deploy would see a "changed" config
	// and needlessly recreate the container. The hash must be stable.
	first := containerConfigHash(containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs))
	for i := 0; i < 50; i++ {
		got := containerConfigHash(containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs))
		require.Equal(t, first, got)
	}
}

func TestContainerRefusesPrivilegeEscalation(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	// A setuid binary in the container (planted by the tenant, who is root there)
	// must not let a process gain privileges beyond what it started with. Caps are
	// deliberately NOT dropped: the app is container-root on purpose (apt-get,
	// binding port 80), and the uid map already keeps those caps off the host.
	assert.Contains(t, strings.Join(args, " "), "--security-opt no-new-privileges")
}

func TestContainerRunsUnconfinedByAppArmor(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := containerCreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", 0, testIDs)
	// podman's default AppArmor profile forbids signals across its per-container
	// "//&crun" label, and the multithreaded Go agent ends up straddling that
	// label -- so "stop app" (agent SIGKILLing its own child) is silently denied
	// by AppArmor and the app never dies. The userns map, no-new-privileges and
	// per-app network already isolate the container, so we run it unconfined.
	assert.Contains(t, strings.Join(args, " "), "--security-opt apparmor=unconfined")
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
