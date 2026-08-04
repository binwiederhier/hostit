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
      python3 python3-venv python3-pip \
      nodejs npm \
      php-cli \
      golang-go \
    && rm -rf /var/lib/apt/lists/*
CMD ["/bin/bash"]
`

	// WorkspaceRuntimes is what the workspace image ships, quoted verbatim to
	// users and agents so nobody has to guess what is available
	WorkspaceRuntimes = "python3 (with venv and pip), node and npm, php-cli, the go toolchain (go build works in here), plus git, curl, rsync, htop, vim and nano"
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
	containerHome := containerHomeDir(a.Name)
	args = append(args,
		"--env", fmt.Sprintf("PORT=%d", a.Port),
		"--env", "HOME="+containerHome,
		"--workdir", containerHome,
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", a.Port, a.Port),
		"--volume", home+":"+containerHome)
	for _, k := range sortedKeys(conf.Env) {
		args = append(args, "--env", k+"="+conf.Env[k])
	}
	args = appendCommonMounts(args, socketFile, hostitBin)
	args = append(args, workspaceImageTag(), hostitBin, "agent")
	return args
}

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
		"--volume", socketDir+":"+socketDir)
}

// absVolume resolves a relative volume source against the app home dir
func absVolume(volume, home string) string {
	src, rest, found := strings.Cut(volume, ":")
	if !found {
		return volume
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(home, src)
	}
	return src + ":" + rest
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
