package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// execDefaultTimeout bounds a command that did not ask for a limit
	execDefaultTimeout = 60 * time.Second
	// execMaxTimeout is the most any caller may ask for. This runs on the
	// daemon's request path on a one-core box, so a long build belongs in the
	// "prepare:" step, which the agent runs on its own time.
	execMaxTimeout = 5 * time.Minute
	// execGraceTimeout is how much longer the daemon waits than the command is
	// allowed, so timeout(1) inside the container is what stops it
	execGraceTimeout = 15 * time.Second
	// execMaxOutput caps what comes back; a chatty build must not become
	// megabytes of JSON, nor megabytes held in the daemon
	execMaxOutput = 256 * 1024
	// execTimeoutExitCode is what timeout(1) reports when it stopped the command
	execTimeoutExitCode = 124
	// execTailKept is how much of an over-long output is worth keeping: the end,
	// where the error that stopped the build is
	execTailKept = execMaxOutput
)

// ExecResult is what a command left behind
type ExecResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// Exec runs a shell command inside the app's own container and returns what it
// printed. It grants nothing the caller did not already have: an app token can
// upload a file and name it in "run:", so the container already executes
// whatever its owner says. What this adds is a way to see the result, which is
// what compiling on the host needs -- and what SSH gives anyone with a key.
//
// The command runs as the container's root, which is the app's own unprivileged
// uid on the host, in the app's home directory.
func (m *Manager) Exec(name, command string, timeout time.Duration) (*ExecResult, error) {
	if _, err := m.store.App(name); err != nil {
		return nil, err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("%w: no command given", ErrInvalid)
	}
	// One at a time per host: these are builds, and the box has one core
	m.execMu.Lock()
	defer m.execMu.Unlock()

	limit := execTimeout(timeout)
	started := time.Now()
	// The limit is enforced inside the container by timeout(1): killing "podman
	// exec" out here would leave the command running in there, burning the app's
	// memory and CPU with nobody left to notice. The outer bound is a backstop
	// for podman itself hanging, so it has to be the looser of the two.
	out, err := m.runner.RunTimeout(limit+execGraceTimeout, "podman", "exec", "--workdir", containerHomeDir(name),
		containerName(name), "timeout", "--kill-after", "5s", strconv.Itoa(int(limit.Seconds())),
		"/bin/sh", "-lc", command)
	res := &ExecResult{ExitCode: exitCode(err)}
	res.Output, res.Truncated = capOutput(out)
	// timeout(1) exits 124 when it had to stop the command; a kill from the outer
	// bound has no status at all, and only the clock tells us
	if res.ExitCode == execTimeoutExitCode || (err != nil && time.Since(started) >= limit) {
		res.TimedOut = true
	}
	return res, nil
}

// TerminalCommand is what the server runs, on a pty, for a browser terminal: the
// app's own login shell as the app user -- exactly what SSH runs. Going through
// hostit-shell (not straight to "podman exec") means the browser terminal is
// identical to an SSH session: the same banner, the same TERM and colours, the
// same entry into the container. runuser drops from the root daemon to the app
// user so the socket sees the right identity, just as sshd would.
func (m *Manager) TerminalCommand(name string) (string, []string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", nil, err
	}
	return "runuser", []string{"-u", name, "--", userShellFile}, nil
}

// execTimeout keeps a caller's request inside what the host can afford
func execTimeout(requested time.Duration) time.Duration {
	if requested <= 0 {
		return execDefaultTimeout
	}
	return min(requested, execMaxTimeout)
}

// capOutput trims an over-long output to its tail, saying that it did
func capOutput(out string) (string, bool) {
	if len(out) <= execMaxOutput {
		return out, false
	}
	return "[...truncated, showing the last " + fmt.Sprint(execTailKept) + " bytes...]\n" + out[len(out)-execTailKept:], true
}

// exitCode digs the status out of a failed command; -1 means it never ran or
// was killed
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// containerHomeDir is the app's home as seen from inside its container
func containerHomeDir(name string) string {
	return "/home/" + name
}
