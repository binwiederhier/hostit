package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	return r.run(context.Background(), args)
}

// RunTimeout kills the command when it outstays its welcome
func (r *rootRunner) RunTimeout(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.run(ctx, args)
}

func (r *rootRunner) run(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no command given")
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
