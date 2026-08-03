package appctl

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// unitName is the systemd user unit under which every app service runs
	unitName = "hostit-app"
	// buildImageTag is the local image tag used for "build:" mode
	buildImageTag = "localhost/hostit-app:latest"
	// containerName is the podman container name; unique per app since rootless
	// podman namespaces containers per user
	containerName = "hostit-app"
)

// unitFile renders the systemd user unit for the app. In process mode the run
// command executes via a login shell with $PORT set; in container mode the unit
// wraps "podman run" with the assigned port published on loopback.
func unitFile(c *AppConfig, self *SelfInfo, appDir, podman string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=hostit app %s\n", self.Name)
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", appDir)
	if c.Mode() == ModeProcess {
		fmt.Fprintf(&b, "Environment=\"PORT=%d\"\n", self.Port)
		for _, k := range sortedKeys(c.Env) {
			fmt.Fprintf(&b, "Environment=%q\n", k+"="+c.Env[k])
		}
		fmt.Fprintf(&b, "ExecStart=/bin/sh -lc '%s'\n", strings.ReplaceAll(c.Run, "'", `'\''`))
	} else {
		fmt.Fprintf(&b, "ExecStartPre=-%s rm --force %s\n", podman, containerName)
		fmt.Fprintf(&b, "ExecStart=%s %s\n", podman, strings.Join(podmanRunArgs(c, self, appDir), " "))
		fmt.Fprintf(&b, "ExecStop=%s stop --time 5 %s\n", podman, containerName)
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=2\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// podmanRunArgs builds the "podman run" arguments for container mode
func podmanRunArgs(c *AppConfig, self *SelfInfo, appDir string) []string {
	args := []string{"run", "--rm", "--name", containerName}
	args = append(args, "--env", fmt.Sprintf("PORT=%d", c.ContainerPort))
	for _, k := range sortedKeys(c.Env) {
		args = append(args, "--env", k+"="+c.Env[k])
	}
	args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", self.Port, c.ContainerPort))
	for _, volume := range c.Volumes {
		args = append(args, "--volume", absVolume(volume, appDir))
	}
	args = append(args, c.imageRef())
	return args
}

// imageRef returns the image to run: the configured one, or the locally built tag
func (c *AppConfig) imageRef() string {
	if c.Build != "" {
		return buildImageTag
	}
	return c.Image
}

// absVolume resolves a relative volume source against the app dir
func absVolume(volume, appDir string) string {
	src, rest, found := strings.Cut(volume, ":")
	if !found {
		return volume
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(appDir, src)
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
