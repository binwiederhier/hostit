package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/run"
)

// requiredBinaries are the external commands the hostit daemon shells out to.
// A missing one otherwise surfaces lazily, mid-operation, as a cryptic error, so
// the preflight checks them all up front.
var requiredBinaries = []string{
	"podman", "btrfs", "nft", "systemctl",
	"useradd", "usermod", "userdel", "groupadd", "groupmod", "groupdel", "pkill",
}

// checkHostRequirements verifies the two non-negotiable prerequisites the daemon
// has before it touches anything: it runs as root, and every command it drives is
// installed. It reports all missing commands at once so an operator fixes them in
// one pass rather than one lazy failure at a time.
func checkHostRequirements() error {
	if os.Geteuid() != 0 {
		return errors.New("hostit must run as root: it creates Unix users and drives podman, systemd, nftables and btrfs")
	}
	if missing := missingBinaries(exec.LookPath); len(missing) > 0 {
		return fmt.Errorf("hostit requires these commands, please install them: %s", strings.Join(missing, ", "))
	}
	return nil
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

// requireBtrfs fails unless the app homes live on a btrfs filesystem. btrfs is
// mandatory: snapshots, rollback, fork and hard disk quotas are core, not
// optional, and hostit refuses to run without them rather than silently degrading.
func requireBtrfs(appsDir string) error {
	if !btrfs.New(run.New()).IsBtrfs(appsDir) {
		return fmt.Errorf("hostit requires the app homes (%s) to be on a btrfs filesystem, for snapshots, rollback and hard disk quotas; see the install docs", appsDir)
	}
	return nil
}
