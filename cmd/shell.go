package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/urfave/cli/v2"
	"golang.org/x/term"
	"heckel.io/hostit/agent"
	"heckel.io/hostit/appctl"
)

const (
	// enterFile is the sudo-able wrapper that enters the caller's container
	enterFile = "/usr/bin/hostit-enter"
)

var (
	// cmdShell is the login-shell entrypoint (via /usr/bin/hostit-shell): it
	// ensures the app container runs and hands the SSH session to the privileged
	// "hostit enter" helper. It never falls back to a host shell; the container
	// IS the user's environment.
	cmdShell = &cli.Command{
		Name:            "shell",
		Hidden:          true,
		SkipFlagParsing: true, // sshd passes "-c <command>"; don't let urfave eat it
		Action:          execShell,
	}

	// cmdAgent is PID 1 inside workspace containers: it supervises the app's
	// run command (see the agent package)
	cmdAgent = &cli.Command{
		Name:   "agent",
		Hidden: true,
		Action: execAgent,
	}
)

func execShell(c *cli.Context) error {
	ctl := appctl.NewController(appctl.DefaultSocketFile())

	// Identify the app and make sure its container is up
	self, err := ctl.Self()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostit: cannot identify app: %s\n", err.Error())
		return cli.Exit("", 1)
	}
	if _, err := ctl.Ensure(); err != nil {
		fmt.Fprintf(os.Stderr, "hostit: cannot start workspace for %s: %s\n", self.Name, err.Error())
		return cli.Exit("", 1)
	}

	// Hand over to the privileged helper, which execs us into our container
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "-"
	}
	args := []string{"sudo", "-n", enterFile, termName}
	args = append(args, c.Args().Slice()...)
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo not found: %w", err)
	}
	return syscall.Exec(sudo, args, os.Environ())
}

func execAgent(_ *cli.Context) error {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	return agent.New(home).Run()
}

// isTerminal reports whether the given file is a terminal, which decides whether
// podman gets a TTY (interactive login) or not (scp, sftp, remote commands)
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
