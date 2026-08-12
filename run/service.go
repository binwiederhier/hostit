// Package run provides the system-command runner that hostit's service packages
// (btrfs, systemd, container) shell out through, defined once here instead of
// redeclared in each. The daemon performs all container and service work itself,
// as root, on behalf of app users.
package run

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a command on the host and returns its combined output.
type Runner interface {
	Run(args ...string) (string, error)
	// RunTimeout is for calls whose answer is nice to have but must not block a
	// request: podman serializes on its own lock, so a slow create or pull would
	// otherwise stall everything that asks for state.
	RunTimeout(timeout time.Duration, args ...string) (string, error)
}

// rootRunner is the real Runner: it execs commands as the (root) daemon.
type rootRunner struct{}

var _ Runner = (*rootRunner)(nil)

// New returns the real, root-requiring Runner.
func New() Runner {
	return &rootRunner{}
}

func (r *rootRunner) Run(args ...string) (string, error) {
	return r.run(context.Background(), args)
}

// RunTimeout kills the command when it outstays its welcome.
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

// Nop is a Runner that does nothing and returns empty output; for tests and
// dry-run tooling that must not touch the host.
type Nop struct{}

var _ Runner = Nop{}

func (Nop) Run(_ ...string) (string, error) { return "", nil }

func (Nop) RunTimeout(_ time.Duration, _ ...string) (string, error) { return "", nil }
