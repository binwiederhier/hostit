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
	// unitPrefix is the systemd template unit instantiated per app
	unitTemplate = "hostit-app@"
	// workspaceImage is the default image for run: mode apps, built once into
	// the daemon's (root) image store and shared by every app
	workspaceImage = "localhost/hostit-workspace:1"
	// buildImageTag is the image tag used for build: mode, one per app
	buildImagePrefix = "localhost/hostit-app-"

	// workspaceContainerfile builds the default workspace image: small, but with
	// everything needed for ssh/scp/sftp/rsync sessions and quick demo apps.
	// The hostit binary itself is bind-mounted, not baked in.
	workspaceContainerfile = `FROM docker.io/library/debian:stable-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bash ca-certificates curl git less openssh-sftp-server procps python3 rsync vim-tiny \
    && rm -rf /var/lib/apt/lists/*
CMD ["/bin/bash"]
`
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

// containerName returns the container name of an app
func containerName(appName string) string {
	return containerPrefix + appName
}

// unitName returns the systemd unit instance of an app
func unitName(appName string) string {
	return unitTemplate + appName
}

// buildImageTag returns the image tag for a build:-mode app
func buildImageTag(appName string) string {
	return buildImagePrefix + appName + ":latest"
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
	args = append(args,
		"--uidmap", fmt.Sprintf("0:%d:1", ids.UID),
		"--uidmap", fmt.Sprintf("1:%d:%d", ids.SubUID, ids.SubCount),
		"--gidmap", fmt.Sprintf("0:%d:1", ids.GID),
		"--gidmap", fmt.Sprintf("1:%d:%d", ids.SubGID, ids.SubCount),
		"--network", "slirp4netns")
	if memoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(memoryMB)+"m")
	}
	if conf == nil || conf.Mode() == appctl.ModeProcess {
		containerHome := "/home/" + a.Name
		args = append(args,
			"--env", fmt.Sprintf("PORT=%d", a.Port),
			"--env", "HOME="+containerHome,
			"--workdir", containerHome,
			"--publish", fmt.Sprintf("127.0.0.1:%d:%d", a.Port, a.Port),
			"--volume", home+":"+containerHome)
		args = appendCommonMounts(args, socketFile, hostitBin)
		args = append(args, workspaceImage, hostitBin, "agent")
		return args
	}
	args = append(args,
		"--env", fmt.Sprintf("PORT=%d", conf.ContainerPort),
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", a.Port, conf.ContainerPort))
	for _, k := range sortedKeys(conf.Env) {
		args = append(args, "--env", k+"="+conf.Env[k])
	}
	for _, volume := range conf.Volumes {
		args = append(args, "--volume", absVolume(volume, home))
	}
	args = appendCommonMounts(args, socketFile, hostitBin)
	args = append(args, imageRef(conf, a.Name))
	return args
}

// containerConfigHash returns a short hash of the container configuration; it is
// stored as a label so the daemon can decide whether a recreate is needed
func containerConfigHash(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// imageRef returns the image an image/build mode app runs
func imageRef(conf *appctl.AppConfig, appName string) string {
	if conf.Build != "" {
		return buildImageTag(appName)
	}
	return conf.Image
}

func appendCommonMounts(args []string, socketFile, hostitBin string) []string {
	return append(args,
		"--volume", hostitBin+":"+hostitBin+":ro",
		"--volume", socketFile+":"+socketFile)
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
