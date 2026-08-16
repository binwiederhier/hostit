// Package workspace owns the workspace container: the podman create spec every
// app container is built from (id-keyed names, uid mappings, mounts, config
// hash) and the storage those containers run -- the workspace image (its
// Containerfile, content-derived tags, and the build/prune lifecycle in
// Service), the per-tag base subvolumes exported from it, and the per-app
// subvolumes: one writable subvolume per app, the full OS tree its container
// runs via --rootfs, with the app's files at home/app inside it. It is pure
// app-container policy with no callbacks into the app orchestration.
package workspace

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
	// ContainerPrefix names an app's container; the daemon runs containers as
	// root, so names share one namespace and must carry the app's (stable) id
	ContainerPrefix = "hostit-app-"
	// UnitTemplate is the systemd template unit instantiated per app
	UnitTemplate = "hostit-app@"
	// FilesDir is where an app's files live INSIDE its subvolume: the subvolume
	// is the container's rootfs, so this host-side path and the in-container
	// home (ContainerHome) are the same tree. It is a fixed path (not the app
	// name) so a rename never moves anything.
	FilesDir = "home/app"
	// ContainerHome is the app's home as seen from inside its container: the
	// files directory at its absolute in-container path.
	ContainerHome = "/" + FilesDir
	// containerPort is what an app listens on inside its own network namespace.
	// Every app has the whole namespace to itself, so they can all use the same
	// obvious number and never see the loopback port hostit picked outside.
	containerPort = 80
	// maxProcesses caps how many processes an app may have. Generous for a build
	// (compilers fan out) and far below what it takes to exhaust the host.
	maxProcesses = 512
	// UIDBlockSize is how many host uids each app owns: a contiguous block whose
	// base is container-root. 65536 so the container has a full uid range (up to
	// nobody). A single contiguous block keeps the map one uniform offset, so
	// every in-container uid lands inside the app's own block and the idmapped
	// rootfs mount is a single uniform mapping too.
	UIDBlockSize = 65536
	// UIDBlockStart is the first app's base uid, high above system users; blocks
	// are spaced UIDBlockSize apart (by port) so they never overlap.
	UIDBlockStart = 1_000_000

	// Runtimes is what the workspace image ships, quoted verbatim to
	// users and agents so nobody has to guess what is available. It is kept lean
	// on purpose (a big image makes every per-app container slower to create and
	// heavier on disk); anything else installs with apt-get.
	Runtimes = "python3 (with venv and pip), the go toolchain (go build works in here), Node.js with npm, PHP (php-cli), sqlite3 for persistent data, plus git, curl, rsync, htop, vim and nano (install anything else with apt-get)"
)

const (
	// containerArgsTrailer is how many elements at the end of CreateArgs are the
	// rootfs and its command rather than options: ["--rootfs", path, hostitBin,
	// "agent"]. podman's flag order is critical here: an option placed after
	// --rootfs is treated as the container command, so every option must come
	// before this trailer.
	containerArgsTrailer = 4
)

// IDs is the app's contiguous host uid/gid block. Container uid 0 maps to UID on
// the host and the block runs Count ids up from there. Being one contiguous
// range matters: the mapping is a single uniform offset, so the idmapped rootfs
// mount maps the whole root-owned tree in one rule and every in-container uid
// stays inside the app's own range (a split "0:uid:1 + 1:subuid:N" map would not).
type IDs struct {
	UID   int
	GID   int
	Count int
}

// ContainerName and UnitName are keyed on the app's stable id, not its name, so a
// rename never recreates the (stateful) container or its unit. Callers holding
// only a name resolve the id first (the app.Manager's name-to-id lookup); callers
// holding the app use these directly.
func ContainerName(id string) string {
	return ContainerPrefix + id
}

// UIDFor is an app's base uid: a contiguous UIDBlockSize-wide block, one per
// app, spaced by port so blocks never overlap. Container uid 0 maps here.
// Both halves of the platform derive it from the same formula.
func UIDFor(portMin, port int) int {
	return UIDBlockStart + (port-portMin)*UIDBlockSize
}

func UnitName(id string) string {
	return UnitTemplate + id
}

// CreateArgs returns the "podman create ..." arguments (without the
// leading podman) for an app's container.
//
// Containers are created by the root daemon but mapped so that container root is
// the app's unprivileged host uid: files in the app subvolume belong to the
// app both inside and outside, and a workload escape lands on that uid rather
// than on root. Each app gets its own network stack (slirp4netns), so containers
// cannot reach each other, and ports are published on loopback only.
//
// The container runs the app's persistent subvolume (--rootfs), not an image:
// the app's files live at home/app inside that same tree (no home bind mount),
// so recreating the container (config change, daemon upgrade) keeps the files
// and whatever the app installed, and one subvolume is the app's disk budget.
func CreateArgs(conf *appctl.AppConfig, a *store.App, subvol, socketFile, hostitBin, version string, memoryMB int, ids IDs) []string {
	args := []string{"create", "--name", ContainerName(a.ID), "--hostname", a.Name}
	// conmon signals readiness to systemd, so the app's Type=notify unit only reports
	// active once the container is actually running. Without this a deploy can race a
	// just-created app whose container is still starting and try to reload (SIGHUP) a
	// container that is not up yet, which podman rejects. The default "container"
	// policy would instead make systemd wait on a READY the agent never sends.
	args = append(args, "--sdnotify", "conmon")
	// Part of the container's identity, so an upgrade makes it stale and apply
	// recreates it: the bind-mounted binary is a file, and a running container
	// keeps the inode it started with
	args = append(args, "--label", "hostit.version="+version)
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
		"--env", "HOME="+ContainerHome,
		"--workdir", ContainerHome,
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", a.Port, containerPort))
	// conf is nil for an app with no usable hostit.yml: the container still comes
	// up, so its owner can SSH in and fix it
	if conf != nil {
		for _, k := range sortedKeys(conf.Env) {
			args = append(args, "--env", k+"="+conf.Env[k])
		}
	}
	args = appendCommonMounts(args, socketFile, hostitBin)
	// The trailer: the rootfs, then the command podman runs in it. Everything
	// before it is an option, so a late-added label (WithConfigLabel) goes in just
	// ahead of it; anything AFTER --rootfs would be taken as the container command.
	// :idmap maps the root-owned subvolume through the container's uid mapping
	// (disk root <-> container root), so no ownership is ever baked into the tree;
	// requires a crun new enough to idmap a rootfs (preflight enforces it).
	args = append(args, "--rootfs", subvol+":idmap", hostitBin, "agent")
	return args
}

// ConfigHash fingerprints the container's configuration (stored as a label), so apply
// recreates it only when something load-bearing changed. The --hostname is
// deliberately excluded: it is cosmetic and derived from the app's mutable name,
// and a rename must never recreate the (stateful) container. Everything else in
// the create args keys on stable things -- the app id, its port, its uid.
func ConfigHash(args []string) string {
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

// WithConfigLabel inserts the config-hash label into create args. A label is an
// option, so it must go before --rootfs; inserting it just ahead of the trailing
// rootfs+command survives new flags being added at the front (the common change)
// rather than depending on a fixed offset into the leading flags.
func WithConfigLabel(args []string, hash string) []string {
	cut := len(args) - containerArgsTrailer
	out := make([]string, 0, len(args)+2)
	out = append(out, args[:cut]...)
	out = append(out, "--label", "hostit.config="+hash)
	out = append(out, args[cut:]...)
	return out
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
