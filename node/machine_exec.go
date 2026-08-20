package node

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/workspace"
)

const (
	// ExecDefaultTimeout bounds a command that did not ask for a limit
	ExecDefaultTimeout = 60 * time.Second
	// ExecMaxTimeout is the most any caller may ask for. This runs on the
	// daemon's request path on a one-core box, so a long build belongs in the
	// "prepare:" step, which the agent runs on its own time.
	ExecMaxTimeout = 5 * time.Minute
	// ExecGraceTimeout is how much longer the daemon waits than the command is
	// allowed, so timeout(1) inside the container is what stops it
	ExecGraceTimeout = 15 * time.Second
	// ExecMaxOutput caps what comes back; a chatty build must not become
	// megabytes of JSON, nor megabytes held in the daemon
	ExecMaxOutput = 256 * 1024
	// ExecTimeoutExitCode is what timeout(1) reports when it stopped the command
	ExecTimeoutExitCode = 124
	// execTailKept is how much of an over-long output is worth keeping: the end,
	// where the error that stopped the build is
	execTailKept = ExecMaxOutput
)

// Exec runs a shell command inside the app's own container and returns what it
// printed. It grants nothing the caller did not already have: an app token can
// upload a file and name it in "run:", so the container already executes
// whatever its owner says. What this adds is a way to see the result, which is
// what compiling on the host needs -- and what SSH gives anyone with a key.
//
// The command runs as the container's root, which is the app's own unprivileged
// uid on the host, in the app's home directory.
func (m *Machine) Exec(name, command string, timeout time.Duration) (*nodeapi.ExecResult, error) {
	// Exec needs a running container, so it enters like a login does: a fresh
	// fork or crashed app is brought up first (instead of racing the background
	// start into a podman error), and a deliberately powered-off app is refused
	// with ErrPoweredOff (the API's 409) rather than podman noise in the output.
	if _, err := m.Ensure(name); err != nil {
		return nil, err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("%w: no command given", nodeapi.ErrInvalid)
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
	out, err := m.container.Exec(limit+ExecGraceTimeout, m.ContainerName(name), workspace.ContainerHome,
		"timeout", "--kill-after", "5s", strconv.Itoa(int(limit.Seconds())),
		"/bin/sh", "-lc", command)
	res := &nodeapi.ExecResult{ExitCode: exitCode(err)}
	res.Output, res.Truncated = capOutput(out)
	// timeout(1) exits 124 when it had to stop the command; a kill from the outer
	// bound has no status at all, and only the clock tells us
	if res.ExitCode == ExecTimeoutExitCode || (err != nil && time.Since(started) >= limit) {
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
func (m *Machine) terminalCommand(name string) (string, []string, error) {
	if _, err := m.store.App(name); err != nil {
		return "", nil, err
	}
	return "runuser", []string{"-u", name, "--", userShellFile}, nil
}

// Terminal starts the login shell on a pty ON THIS MACHINE, which is the only
// place "runuser <app>" means anything -- the user exists here and nowhere
// else. Its predecessor returned the command for the caller to exec, which was
// wrong the moment the caller stopped being on the app's machine: control ran
// "runuser watchme" on its own host and got "user does not exist".
func (m *Machine) Terminal(name string) (nodeapi.TerminalSession, error) {
	prog, args, err := m.terminalCommand(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(prog, args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &localTerminal{ptmx: ptmx, cmd: cmd}, nil
}

// localTerminal is a pty-backed session on the app's own machine.
type localTerminal struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (t *localTerminal) Read(p []byte) (int, error)  { return t.ptmx.Read(p) }
func (t *localTerminal) Write(p []byte) (int, error) { return t.ptmx.Write(p) }

func (t *localTerminal) Resize(cols, rows uint16) error {
	return pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

func (t *localTerminal) Close() error {
	_ = t.ptmx.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return nil
}

// execTimeout keeps a caller's request inside what the host can afford
func execTimeout(requested time.Duration) time.Duration {
	if requested <= 0 {
		return ExecDefaultTimeout
	}
	return min(requested, ExecMaxTimeout)
}

// capOutput trims an over-long output to its tail, saying that it did
func capOutput(out string) (string, bool) {
	if len(out) <= ExecMaxOutput {
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
