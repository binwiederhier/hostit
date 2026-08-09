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
	// root, so names share one namespace and must carry the app's (stable) id
	containerPrefix = "hostit-app-"
	// unitTemplate is the systemd template unit instantiated per app
	unitTemplate = "hostit-app@"
	// containerHome is the app's home as seen from inside its container. It is a
	// fixed path (not the app name) so a rename never has to recreate the container
	// to fix it: the id-keyed host home is bind-mounted here and stays put.
	containerHome = "/home/app"
	// containerPort is what an app listens on inside its own network namespace.
	// Every app has the whole namespace to itself, so they can all use the same
	// obvious number and never see the loopback port hostit picked outside.
	containerPort = 80
	// maxProcesses caps how many processes an app may have. Generous for a build
	// (compilers fan out) and far below what it takes to exhaust the host.
	maxProcesses = 512
	// uidBlockSize is how many host uids each app owns: a contiguous block whose
	// base is container-root. 65536 so the container has a full uid range (up to
	// nobody). A single contiguous block is what lets podman idmap-mount the image.
	uidBlockSize = 65536
	// uidBlockStart is the first app's base uid, high above system users; blocks
	// are spaced uidBlockSize apart (by port) so they never overlap.
	uidBlockStart = 1_000_000
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
      golang-go \
      nodejs npm \
      php-cli \
    && rm -rf /var/lib/apt/lists/*
# System-wide shell niceties so every login shell (SSH and the web terminal)
# gets the usual colours and ll/la aliases, without a dotfile in the app's home.
# Written from base64 so the escapes survive the Containerfile (a heredoc RUN is
# not portable across buildah versions); the decoded file is:
#   alias ls='ls --color=auto'; alias ll='ls -alF'; alias la='ls -A'; alias l='ls -CF'
#   alias grep='grep --color=auto'; dircolors; a coloured PS1
RUN echo YWxpYXMgbHM9J2xzIC0tY29sb3I9YXV0bycKYWxpYXMgbGw9J2xzIC1hbEYnCmFsaWFzIGxhPSdscyAtQScKYWxpYXMgbD0nbHMgLUNGJwphbGlhcyBncmVwPSdncmVwIC0tY29sb3I9YXV0bycKWyAteCAvdXNyL2Jpbi9kaXJjb2xvcnMgXSAmJiBldmFsICIkKGRpcmNvbG9ycyAtYikiCmV4cG9ydCBQUzE9J1xbXDAzM1swMTszMm1cXVx1QFxoXFtcMDMzWzAwbVxdOlxbXDAzM1swMTszNG1cXVx3XFtcMDMzWzAwbVxdXCQgJwo= | base64 -d > /etc/profile.d/hostit.sh
CMD ["/bin/bash"]
`

	// WorkspaceRuntimes is what the workspace image ships, quoted verbatim to
	// users and agents so nobody has to guess what is available. It is kept lean
	// on purpose (a big image makes every per-app container slower to create and
	// heavier on disk); anything else installs with apt-get.
	WorkspaceRuntimes = "python3 (with venv and pip), the go toolchain (go build works in here), Node.js with npm, PHP (php-cli), sqlite3 for persistent data, plus git, curl, rsync, htop, vim and nano (install anything else with apt-get)"
)

// IDs is the app's contiguous host uid/gid block. Container uid 0 maps to UID on
// the host and the block runs Count ids up from there. Being one contiguous
// range matters: it is a single uniform offset, so podman uses a kernel
// idmapped mount (instant, no copy) instead of chowning a private copy of the
// whole image for the mapping (which a split "0:uid:1 + 1:subuid:N" map forces).
type IDs struct {
	UID   int
	GID   int
	Count int
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

// containerName and unitName are keyed on the app's stable id, not its name, so a
// rename never recreates the (stateful) container or its unit. Callers holding
// only a name go through the method, which resolves the id; callers holding the
// app use the ForID form directly.
func containerNameForID(id string) string {
	return containerPrefix + id
}

func unitNameForID(id string) string {
	return unitTemplate + id
}

func (m *Manager) containerName(name string) string {
	return containerNameForID(m.appID(name))
}

func (m *Manager) unitName(name string) string {
	return unitNameForID(m.appID(name))
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
	args := []string{"create", "--name", containerNameForID(a.ID), "--hostname", a.Name}
	// Part of the container's identity, so an upgrade makes it stale and apply
	// recreates it: the bind-mounted binary is a file, and a running container
	// keeps the inode it started with
	args = append(args, "--label", "hostit.version="+Version)
	args = append(args,
		"--uidmap", fmt.Sprintf("0:%d:%d", ids.UID, ids.Count),
		"--gidmap", fmt.Sprintf("0:%d:%d", ids.GID, ids.Count),
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
	// podman's default AppArmor profile mediates signals by a per-container
	// "//&crun" label, and the multithreaded Go agent straddles that label, so its
	// SIGKILL to a child ("stop app", reload) is denied and the app never dies --
	// leaving a duplicate fighting for the port. The uid map, no-new-privileges and
	// per-app network already isolate the container, so run it unconfined.
	args = append(args, "--security-opt", "apparmor=unconfined")
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
	// The app is pinned to the image tag it was built with; an app from before
	// pinning (empty tag) falls back to the current image.
	image := a.ImageTag
	if image == "" {
		image = workspaceImageTag()
	}
	args = append(args, image, hostitBin, "agent")
	return args
}

// containerArgsTrailer is how many elements at the end of containerCreateArgs are
// the image and its command rather than flags: [imageTag, hostitBin, "agent"].
const containerArgsTrailer = 3

// containerConfigHash returns a short hash of the container configuration; it is
// stored as a label so the daemon can decide whether a recreate is needed
// containerConfigHash fingerprints the container's configuration, so apply
// recreates it only when something load-bearing changed. The --hostname is
// deliberately excluded: it is cosmetic and derived from the app's mutable name,
// and a rename must never recreate the (stateful) container. Everything else in
// the create args keys on stable things -- the app id, its port, its uid.
func containerConfigHash(args []string) string {
	hashable := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--hostname" && i+1 < len(args) {
			i++ // skip the flag and its (name-derived) value
			continue
		}
		hashable = append(hashable, args[i])
	}
	sum := sha256.Sum256([]byte(strings.Join(hashable, "\x00")))
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
