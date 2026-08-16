package preflight

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/run"
)

const (
	// minPodmanMajor/Minor is the first podman with the --rootfs <path>:idmap
	// syntax every app container uses.
	minPodmanMajor, minPodmanMinor = 4, 3
	// minCrunMajor/Minor is the crun the idmapped rootfs was validated with
	// (1.14.1, what Ubuntu 24.04 ships, hard-fails on it; see the install docs
	// for dropping in the static release binary).
	minCrunMajor, minCrunMinor = 1, 29
)

// requiredBinaries are the external commands the hostit daemon shells out to.
// A missing one otherwise surfaces lazily, mid-operation, as a cryptic error, so
// the preflight checks them all up front.
var requiredBinaries = []string{
	"podman", "btrfs", "nft", "systemctl",
	"useradd", "usermod", "userdel", "groupadd", "groupmod", "groupdel", "pkill",
}

// CheckHost verifies the two non-negotiable prerequisites the daemon
// has before it touches anything: it runs as root, and every command it drives is
// installed. It reports all missing commands at once so an operator fixes them in
// one pass rather than one lazy failure at a time.
func CheckHost() error {
	if os.Geteuid() != 0 {
		return errors.New("hostit must run as root: it creates Unix users and drives podman, systemd, nftables and btrfs")
	}
	if missing := missingBinaries(exec.LookPath); len(missing) > 0 {
		return fmt.Errorf("hostit requires these commands, please install them: %s", strings.Join(missing, ", "))
	}
	return checkRuntimeVersions()
}

// checkRuntimeVersions refuses to start on a container runtime that cannot
// idmap-mount a rootfs: every app container depends on it, and the failure
// would otherwise surface per-app as podman noise. podman must speak the
// --rootfs :idmap syntax, and the OCI runtime must be a new enough crun --
// resolved through podman itself, so a containers.conf override (the documented
// way to ship a newer static crun) is what gets checked.
func checkRuntimeVersions() error {
	out, err := exec.Command("podman", "--version").Output()
	if err != nil {
		return fmt.Errorf("cannot run podman --version: %w", err)
	}
	podmanVersion, err := parseRuntimeVersion(string(out), "podman")
	if err != nil {
		return err
	}
	if !versionAtLeast(podmanVersion, minPodmanMajor, minPodmanMinor) {
		return fmt.Errorf("hostit requires podman >= %d.%d for idmapped rootfs mounts, found %s; see the install docs", minPodmanMajor, minPodmanMinor, podmanVersion)
	}
	path, err := exec.Command("podman", "info", "--format", "{{.Host.OCIRuntime.Path}}").Output()
	if err != nil {
		return fmt.Errorf("cannot resolve the OCI runtime via podman info: %w", err)
	}
	out, err = exec.Command(strings.TrimSpace(string(path)), "--version").Output()
	if err != nil {
		return fmt.Errorf("cannot run %s --version: %w", strings.TrimSpace(string(path)), err)
	}
	crunVersion, err := parseRuntimeVersion(string(out), "crun")
	if err != nil {
		return err
	}
	if !versionAtLeast(crunVersion, minCrunMajor, minCrunMinor) {
		return fmt.Errorf("hostit requires crun >= %d.%d for idmapped rootfs mounts, found %s; see the install docs for the static release binary", minCrunMajor, minCrunMinor, crunVersion)
	}
	return nil
}

// parseRuntimeVersion extracts the semver from a "<name> version X.Y.Z" line,
// refusing output from a different tool (a runc configured where crun belongs).
func parseRuntimeVersion(out, name string) (string, error) {
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != name || fields[1] != "version" {
		return "", fmt.Errorf("unexpected %s --version output: %q", name, line)
	}
	// Distro suffixes ("1.14.1-1ubuntu1") are noise past the semver.
	version := strings.SplitN(fields[2], "-", 2)[0]
	return version, nil
}

// versionAtLeast reports whether a "X.Y[.Z]" version is at least major.minor;
// anything unparseable fails closed.
func versionAtLeast(version string, major, minor int) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

// missingBinaries returns the required binaries not found via lookPath, in order.
func missingBinaries(lookPath func(string) (string, error)) []string {
	var missing []string
	for _, b := range requiredBinaries {
		if _, err := lookPath(b); err != nil {
			missing = append(missing, b)
		}
	}
	return missing
}

// RequireBtrfs fails unless the app subvolumes live on a btrfs filesystem. btrfs is
// mandatory: snapshots, rollback, fork and hard disk quotas are core, not
// optional, and hostit refuses to run without them rather than silently degrading.
func RequireBtrfs(appsDir string) error {
	if !btrfs.New(run.New()).IsBtrfs(appsDir) {
		return fmt.Errorf("hostit requires the apps directory (%s) to be on a btrfs filesystem, for snapshots, rollback and hard disk quotas; see the install docs", appsDir)
	}
	return nil
}

// Preflight is the host check for any machine-half daemon (the fused daemon
// and hostit-node): root, required commands, runtime versions, btrfs.
func Check(appsDir string) error {
	if err := CheckHost(); err != nil {
		return err
	}
	return RequireBtrfs(appsDir)
}
