package app

import (
	"fmt"
	"os/exec"
	"strings"
)

// userRunner is the real UserRunner: it executes commands as the app user via
// runuser, with the environment rootless podman and systemctl --user require
type userRunner struct {
	ops SystemOps
}

var _ UserRunner = (*userRunner)(nil)

// NewUserRunner returns the real, root-requiring UserRunner
func NewUserRunner(ops SystemOps) UserRunner {
	return &userRunner{ops: ops}
}

func (r *userRunner) RunAsUser(username string, args ...string) (string, error) {
	uid, err := r.ops.LookupUID(username)
	if err != nil {
		return "", err
	}
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)
	full := []string{
		"-u", username, "--", "env",
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
	}
	full = append(full, args...)
	cmd := exec.Command("runuser", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("as %s: %s failed: %w: %s", username, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
