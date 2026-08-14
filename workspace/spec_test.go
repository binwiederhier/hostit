package workspace

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

const (
	testVersion = "v1.2.3-test"
)

var testIDs = IDs{UID: 1001, GID: 1001, Count: 65536}

func TestContainerCreateArgsWorkspaceMode(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "python3 -m http.server $PORT"}
	require.NoError(t, conf.Validate())
	a := &store.App{ID: "appid123", Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/srv/hostit/apps/appid123", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs)
	cmd := strings.Join(args, " ")
	// The container is named by the app's stable id, not its name, so a rename never
	// has to recreate it. The in-container home is a fixed path for the same reason.
	assert.Contains(t, cmd, "create --name hostit-app-appid123")
	assert.Contains(t, cmd, "--hostname blog")
	assert.Contains(t, cmd, "--env PORT=80")
	assert.Contains(t, cmd, "--env HOME=/home/app")
	assert.Contains(t, cmd, "--workdir /home/app")
	assert.Contains(t, cmd, "--publish 127.0.0.1:10000:80")
	// A single contiguous id block, container 0 -> host 1001. The app subvolume
	// is chowned to this block at creation, so a plain --rootfs works without any
	// idmap-mount support from the runtime.
	assert.Contains(t, cmd, "--uidmap 0:1001:65536")
	assert.Contains(t, cmd, "--gidmap 0:1001:65536")
	assert.NotContains(t, cmd, "--uidmap 0:1001:1")
	// NO home bind: /home/app lives inside the app subvolume the container runs
	// as its rootfs, so the only volumes left are the hostit binary and socket dir.
	assert.NotContains(t, cmd, ":/home/app")
	assert.Contains(t, cmd, "--volume /usr/bin/hostit:/usr/bin/hostit:ro")
	assert.Contains(t, cmd, "--volume /run/hostit:/run/hostit")
	// The container runs the app's persistent subvolume, not an image: no image
	// tag anywhere in the argv.
	assert.NotContains(t, cmd, ImageTag())
	assert.NotContains(t, cmd, imagePrefix)
	// The agent supervises the run command as PID 1; podman's flag order is
	// load-bearing: everything after --rootfs <path> is the container command.
	assert.True(t, strings.HasSuffix(cmd, "--rootfs /srv/hostit/apps/appid123 /usr/bin/hostit agent"), cmd)
}

func TestCreateArgsOptionsAllPrecedeTheRootfs(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server", Env: map[string]string{"K": "v"}}
	a := &store.App{ID: "appid123", Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/apps/appid123", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 512, testIDs)
	// podman treats anything after --rootfs <path> as the container command, so a
	// single misplaced option silently becomes argv[0] inside the container. The
	// only things after --rootfs must be its path and the agent command.
	rootfsAt := -1
	for i, arg := range args {
		if arg == "--rootfs" {
			rootfsAt = i
		}
	}
	require.GreaterOrEqual(t, rootfsAt, 0)
	assert.Equal(t, []string{"--rootfs", "/apps/appid123", "/usr/bin/hostit", "agent"}, args[rootfsAt:])
	for _, arg := range args[rootfsAt+1:] {
		assert.False(t, strings.HasPrefix(arg, "--"), "option %q after --rootfs would become the container command", arg)
	}
}

func TestCreateArgsHashDiffersFromTheOldImageShape(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	a := &store.App{ID: "appid123", Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/apps/appid123", "/s", "/usr/bin/hostit", testVersion, 0, testIDs)
	// The pre-rootfs argv ended in [imageTag, hostitBin, agent]. The new shape must
	// hash differently, because that hash change is exactly what triggers the
	// one-time recreate of every existing container onto its rootfs.
	oldShape := append(append([]string{}, args[:len(args)-containerArgsTrailer]...), ImageTag(), "/usr/bin/hostit", "agent")
	assert.NotEqual(t, ConfigHash(oldShape), ConfigHash(args))
}

func TestContainerConfigHashChanges(t *testing.T) {
	t.Parallel()
	a := &store.App{Name: "blog", Port: 10000}
	conf1 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	conf2 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	conf3 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./other"}
	conf4 := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server", Env: map[string]string{"K": "v"}}
	hash1 := ConfigHash(CreateArgs(conf1, a, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	hash2 := ConfigHash(CreateArgs(conf2, a, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	hash3 := ConfigHash(CreateArgs(conf3, a, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	hash4 := ConfigHash(CreateArgs(conf4, a, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	assert.Equal(t, hash1, hash2)
	// run: changes are handled by the agent (SIGHUP), not by recreating the container,
	// so the hash must NOT depend on the run command ...
	assert.Equal(t, hash1, hash3)
	// ... but image/port changes must recreate
	assert.NotEqual(t, hash1, hash4)
}

func TestConfigHashIgnoresTheHostname(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	// Same app id (same container name), different display name: only the
	// --hostname differs, and a rename must never recreate the (stateful)
	// container, so the hash must not change.
	before := &store.App{ID: "appid123", Name: "blog", Port: 10000}
	after := &store.App{ID: "appid123", Name: "shop", Port: 10000}
	hashBefore := ConfigHash(CreateArgs(conf, before, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	hashAfter := ConfigHash(CreateArgs(conf, after, "/apps/x", "/s", "/b", testVersion, 0, testIDs))
	assert.Equal(t, hashBefore, hashAfter)
}

func TestContainerMountsTheSocketDirectory(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, IDs{UID: 1001, GID: 1001})
	joined := strings.Join(args, " ")

	// The daemon deletes and recreates its socket on every restart, so a mount
	// of the socket file pins an inode that is already gone: the app's CLI then
	// gets "connection refused" until its container is recreated. Mounting the
	// directory lets the container resolve the current socket by path.
	// Read-only: the app only connects to the socket, it never writes the dir
	assert.Contains(t, joined, "--volume /run/hostit:/run/hostit:ro")
	assert.NotContains(t, joined, "--volume /run/hostit/hostit.sock:/run/hostit/hostit.sock")
}

func TestContainerUsesConmonSdnotify(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	joined := strings.Join(CreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs), " ")
	// conmon (not the container) signals readiness, so the app's Type=notify systemd
	// unit only reports active once the container is actually running -- a deploy that
	// races a just-created app must not try to reload a container that is still
	// starting. Anything but conmon (the default "container") would make systemd wait
	// on a READY the agent never sends.
	assert.Contains(t, joined, "--sdnotify conmon")
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
	first := ConfigHash(CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs))
	for i := 0; i < 50; i++ {
		got := ConfigHash(CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs))
		require.Equal(t, first, got)
	}
}

func TestContainerRefusesPrivilegeEscalation(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs)
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
	args := CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs)
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
	args := CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs)

	// The binary is bind-mounted as a file, so replacing it on the host leaves
	// running containers on the old inode: the app's CLI and its agent stay on
	// the previous version until the container is recreated. Making the version
	// part of the container's identity is what triggers that recreate.
	assert.Contains(t, strings.Join(args, " "), "hostit.version="+testVersion)
	older := CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", "v0.0.1-old", 0, testIDs)
	assert.NotEqual(t, ConfigHash(args), ConfigHash(older),
		"an upgrade must make the container stale")
}

func TestContainerHasProcessAndMemoryLimits(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	joined := strings.Join(CreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 512, testIDs), " ")

	// A fork bomb in one app must not take the host down with it. podman has a
	// default, but a default is the distribution's opinion, not hostit's.
	assert.Contains(t, joined, "--pids-limit "+strconv.Itoa(maxProcesses))
	assert.Contains(t, joined, "--memory 512m")
}

func TestContainerOmitsTheMemoryFlagWhenUnlimited(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeStatic}
	a := &store.App{Name: "blog", Port: 10000}
	// A zero cap means unlimited: no --memory flag at all, rather than "0m".
	joined := strings.Join(CreateArgs(conf, a, "/srv/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs), " ")
	assert.NotContains(t, joined, "--memory")
}

func TestAppsListenOnPortEightyInside(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./bin/server"}
	a := &store.App{Name: "blog", Port: 10007}
	joined := strings.Join(CreateArgs(conf, a, "/var/lib/hostit/apps/blog", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs), " ")

	// The host port is hostit's bookkeeping. Inside its own network namespace an
	// app has :80 to itself, so that is what it should listen on -- nobody should
	// have to care which number the platform picked outside.
	assert.Contains(t, joined, fmt.Sprintf("--publish 127.0.0.1:%d:%d", a.Port, containerPort))
	assert.Contains(t, joined, fmt.Sprintf("--env PORT=%d", containerPort))
	assert.Equal(t, 80, containerPort)
}

func TestWithConfigLabelInsertsBeforeTheTrailer(t *testing.T) {
	t.Parallel()
	conf := &appctl.AppConfig{Mode: appctl.ModeApp, Run: "./server"}
	a := &store.App{ID: "appid123", Name: "blog", Port: 10000}
	args := CreateArgs(conf, a, "/apps/appid123", "/run/hostit/hostit.sock", "/usr/bin/hostit", testVersion, 0, testIDs)
	labeled := WithConfigLabel(args, "abc123")
	// The label is an option, so it must land before the trailing rootfs+command:
	// after --rootfs it would be taken as the container command. Injecting it just
	// ahead of the trailer keeps podman's argv valid whatever flags come before it.
	require.Len(t, labeled, len(args)+2)
	assert.Equal(t, "--label", labeled[len(labeled)-6])
	assert.Equal(t, "hostit.config=abc123", labeled[len(labeled)-5])
	assert.Equal(t, []string{"--rootfs", "/apps/appid123", "/usr/bin/hostit", "agent"}, labeled[len(labeled)-4:])
	// The original args are untouched around the insertion point.
	assert.Equal(t, args[:len(args)-4], labeled[:len(labeled)-6])
}

func TestContainerAndUnitNamesKeyOnTheID(t *testing.T) {
	t.Parallel()
	// Containers and units carry the app's stable id, not its mutable name, so a
	// rename never recreates them; the prefixes are what reverse-parsing keys on.
	assert.Equal(t, "hostit-app-appid123", ContainerName("appid123"))
	assert.Equal(t, "hostit-app@appid123", UnitName("appid123"))
	assert.Equal(t, ContainerPrefix+"appid123", ContainerName("appid123"))
	assert.Equal(t, UnitTemplate+"appid123", UnitName("appid123"))
}
