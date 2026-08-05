package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

const (
	// containerPrefix names an app's container; the daemon runs containers as
	// root, so names share one namespace and must carry the app name
	containerPrefix = "hostit-app-"
	// unitTemplate is the systemd template unit instantiated per app
	unitTemplate = "hostit-app@"
	// containerPort is what an app listens on inside its own network namespace.
	// Every app has the whole namespace to itself, so they can all use the same
	// obvious number and never see the loopback port hostit picked outside.
	containerPort = 80
	// maxProcesses caps how many processes an app may have. Generous for a build
	// (compilers fan out) and far below what it takes to exhaust the host.
	maxProcesses = 512
	// workspaceImagePrefix names the default image for static/run mode apps, built
	// once into the daemon's (root) image store and shared by every app. The tag
	// after it is a hash of the Containerfile, so editing that file is enough to
	// get the new image built and the containers recreated onto it.
	workspaceImagePrefix = "localhost/hostit-workspace"

	// workspaceContainerfile builds the default workspace image: small, but with
	// everything needed for ssh/scp/sftp/rsync sessions and quick demo apps.
	// The hostit binary itself is bind-mounted, not baked in.
	workspaceContainerfile = `FROM docker.io/library/debian:stable-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bash ca-certificates curl git htop less nano openssh-sftp-server procps rsync vim \
      sqlite3 \
      python3 python3-venv python3-pip \
      nodejs npm \
      php-cli php-sqlite3 \
      golang-go \
    && rm -rf /var/lib/apt/lists/*
CMD ["/bin/bash"]
`

	// WorkspaceRuntimes is what the workspace image ships, quoted verbatim to
	// users and agents so nobody has to guess what is available
	WorkspaceRuntimes = "python3 (with venv and pip), node and npm, php-cli, the go toolchain (go build works in here), sqlite3 for persistent data, plus git, curl, rsync, htop, vim and nano"
)

// IDs are the identity ranges a container is mapped into: the app's own uid/gid
// become container root, and the subordinate ranges cover everything above it
type IDs struct {
	UID      int
	GID      int
	SubUID   int
	SubGID   int
	SubCount int
}

// workspaceImageTag is the image built from the current Containerfile
func workspaceImageTag() string {
	return imageTagFor(workspaceContainerfile)
}

// imageTagFor derives a tag from what the image is built out of
func imageTagFor(containerfile string) string {
	sum := sha256.Sum256([]byte(containerfile))
	return workspaceImagePrefix + ":" + hex.EncodeToString(sum[:6])
}

// containerName returns the container name of an app
func containerName(appName string) string {
	return containerPrefix + appName
}

// unitName returns the systemd unit instance of an app
func unitName(appName string) string {
	return unitTemplate + appName
}

// containerCreateArgs returns the "podman create ..." arguments (without the
// leading podman) for an app's container.
//
// Containers are created by the root daemon but mapped so that container root is
// the app's unprivileged host uid: files in the bind-mounted home belong to the
// app both inside and outside, and a workload escape lands on that uid rather
// than on root. Each app gets its own network stack (slirp4netns), so containers
// cannot reach each other, and ports are published on loopback only.
func containerCreateArgs(conf *appctl.AppConfig, a *store.App, home, socketFile, hostitBin string, memoryMB int, ids IDs) []string {
	args := []string{"create", "--name", containerName(a.Name), "--hostname", a.Name}
	// Part of the container's identity, so an upgrade makes it stale and apply
	// recreates it: the bind-mounted binary is a file, and a running container
	// keeps the inode it started with
	args = append(args, "--label", "hostit.version="+Version)
	args = append(args,
		"--uidmap", fmt.Sprintf("0:%d:1", ids.UID),
		"--uidmap", fmt.Sprintf("1:%d:%d", ids.SubUID, ids.SubCount),
		"--gidmap", fmt.Sprintf("0:%d:1", ids.GID),
		"--gidmap", fmt.Sprintf("1:%d:%d", ids.SubGID, ids.SubCount),
		"--network", "slirp4netns")
	if memoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(memoryMB)+"m")
	}
	// A fork bomb in one app must not take the host with it. podman has a
	// default for this, but a default is the distribution's opinion.
	args = append(args, "--pids-limit", strconv.Itoa(maxProcesses))
	// A setuid binary the tenant plants must not escalate beyond where it started.
	// Caps are left in place on purpose: the app is container-root so it can
	// apt-get and bind port 80, and the uid map already keeps those caps off the
	// host, so an escape lands as an unprivileged user regardless.
	args = append(args, "--security-opt", "no-new-privileges")
	containerHome := containerHomeDir(a.Name)
	args = append(args,
		"--env", fmt.Sprintf("PORT=%d", containerPort),
		"--env", "HOME="+containerHome,
		"--workdir", containerHome,
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", a.Port, containerPort),
		"--volume", home+":"+containerHome)
	// conf is nil for an app with no usable hostit.yml: the container still comes
	// up, so its owner can SSH in and fix it
	if conf != nil {
		for _, k := range sortedKeys(conf.Env) {
			args = append(args, "--env", k+"="+conf.Env[k])
		}
	}
	args = appendCommonMounts(args, socketFile, hostitBin)
	// The trailer: image, then the command podman runs in it. Everything before it
	// is a flag, so a late-added label (withConfigLabel) goes in just ahead of it.
	args = append(args, workspaceImageTag(), hostitBin, "agent")
	return args
}

// containerArgsTrailer is how many elements at the end of containerCreateArgs are
// the image and its command rather than flags: [imageTag, hostitBin, "agent"].
const containerArgsTrailer = 3

// containerConfigHash returns a short hash of the container configuration; it is
// stored as a label so the daemon can decide whether a recreate is needed
func containerConfigHash(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// appendCommonMounts adds what every app container needs to talk to hostit: the
// binary, and the directory holding the daemon's socket.
//
// The directory, not the socket itself: the daemon deletes and recreates the
// socket on every start, so a mount of the file pins an inode that is already
// gone, and the app's CLI answers "connection refused" until its container is
// recreated. Mounting the directory lets it resolve the live socket by path.
func appendCommonMounts(args []string, socketFile, hostitBin string) []string {
	socketDir := filepath.Dir(socketFile)
	return append(args,
		"--volume", hostitBin+":"+hostitBin+":ro",
		"--volume", socketDir+":"+socketDir+":ro")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
