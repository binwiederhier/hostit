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

var (
	// cmdShell is the login-shell entrypoint (via /usr/bin/hostit-shell): it
	// ensures the app container runs and execs the SSH session into it. Never
	// falls back to a host shell; the container IS the user's environment.
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
	interactive := term.IsTerminal(int(os.Stdin.Fd()))

	// Identify the app and make sure its container is up; the first login builds
	// the workspace image, which takes a minute
	self, err := ctl.Self()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostit: cannot identify app: %s\n", err.Error())
		return cli.Exit("", 1)
	}
	if interactive {
		fmt.Fprintf(os.Stderr, "hostit: preparing workspace for %s (first login may take a minute) ...\n", self.Name)
	}
	if _, err := ctl.Ensure(); err != nil {
		fmt.Fprintf(os.Stderr, "hostit: cannot start workspace: %s\n", err.Error())
		return cli.Exit("", 1)
	}

	// Exec the session into the container; bash if the image has it, else sh
	args := []string{"podman", "exec", "--interactive"}
	if interactive {
		args = append(args, "--tty")
	}
	if os.Getenv("TERM") != "" {
		args = append(args, "--env", "TERM="+os.Getenv("TERM"))
	}
	args = append(args, "hostit-app")
	if c.NArg() >= 2 && c.Args().Get(0) == "-c" {
		args = append(args, "/bin/sh", "-lc", c.Args().Get(1))
	} else {
		args = append(args, "/bin/sh", "-lc", "command -v bash >/dev/null && exec bash -l; exec sh -l")
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		return fmt.Errorf("podman not found: %w", err)
	}
	return syscall.Exec(podman, args, sessionEnv())
}

func execAgent(_ *cli.Context) error {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	return agent.New(home).Run()
}

// sessionEnv ensures XDG_RUNTIME_DIR is set; sshd sessions have it via
// pam_systemd, but be defensive since podman needs it for rootless mode
func sessionEnv() []string {
	env := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		env = append(env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	return env
}
