package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/store"
)

const (
	// containerName is the per-app container; unique per app because rootless
	// podman namespaces containers per user
	containerName = "hostit-app"
	// workspaceImage is the default image for run: mode apps, built per user
	// from workspaceContainerfile on first use
	workspaceImage = "localhost/hostit-workspace:1"
	// buildImageTag is the per-user image tag used for build: mode
	buildImageTag = "localhost/hostit-app:latest"
	// unitName is the systemd user unit that keeps the app container running
	unitName = "hostit-app"

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

// containerCreateArgs returns the "podman create ..." arguments (without the
// leading podman) for an app's container. Workspace mode runs the agent as PID 1
// in the default image with the home mounted; image/build mode runs the app's
// own image and entrypoint. The hostit binary and daemon socket are mounted in
// both, so the CLI works inside every container.
func containerCreateArgs(conf *appctl.AppConfig, a *store.App, home, socketFile, hostitBin string) []string {
	args := []string{"create", "--name", containerName, "--hostname", a.Name}
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
	args = append(args, imageRef(conf))
	return args
}

// containerConfigHash returns a short hash of the container configuration; it is
// stored as a label so the daemon can decide whether a recreate is needed
func containerConfigHash(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// workspaceUnitFile renders the systemd user unit that keeps the app container
// running (restart on failure, start at boot via lingering)
func workspaceUnitFile(appName, podman string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=hostit app %s\n", appName)
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s start --attach %s\n", podman, containerName)
	fmt.Fprintf(&b, "ExecStop=%s stop --time 5 %s\n", podman, containerName)
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// imageRef returns the image an image/build mode app runs
func imageRef(conf *appctl.AppConfig) string {
	if conf.Build != "" {
		return buildImageTag
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
