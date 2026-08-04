package app

import (
	"fmt"
	"os/exec"
	"strings"
)

// rootRunner is the real Runner: the daemon runs podman and systemctl itself,
// as root, on behalf of app users
type rootRunner struct{}

var _ Runner = (*rootRunner)(nil)

// NewRunner returns the real, root-requiring Runner
func NewRunner() Runner {
	return &rootRunner{}
}

func (r *rootRunner) Run(args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no command given")
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
