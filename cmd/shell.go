package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/urfave/cli/v2"
	"golang.org/x/term"
	"heckel.io/hostit/agent"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/workspace"
)

const (
	// enterFile is the sudo-able wrapper that enters the caller's container
	enterFile = "/usr/bin/hostit-enter"
	// cursor is the wordmark's block, a space on a 24-bit background set to the web
	// app's accent (#159cb0), so the login banner's cursor matches the logo cursor
	// in the dashboard rather than a plain terminal green.
	cursor = "\x1b[48;2;21;156;176m \x1b[0m"
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
		// A powered-off app is not started by a login; say so plainly rather than
		// with a generic failure, so the owner knows to power it on first.
		if errors.Is(err, appctl.ErrPoweredOff) {
			fmt.Fprintf(os.Stderr, "hostit: %s is powered off. Power it on first (from the dashboard, or `hostit apps power on %s`).\n", self.Name, self.Name)
			return cli.Exit("", 1)
		}
		fmt.Fprintf(os.Stderr, "hostit: cannot start workspace for %s: %s\n", self.Name, err.Error())
		return cli.Exit("", 1)
	}

	// Only greet a human: scp, rsync and "ssh host <command>" must see nothing
	// but their own protocol on the wire
	if isTerminal(os.Stdin) && c.NArg() == 0 {
		fmt.Fprint(os.Stdout, loginBanner(self))
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

// loginBanner is what an interactive SSH session sees first: the wordmark (with
// the same accent-coloured cursor as the web app's logo), which app this is, and
// the handful of commands that do anything here.
func loginBanner(self *appctl.SelfInfo) string {
	return "\n" +
		"  _               _   _ _   \n" +
		" | |_   ___  ___ | |_(_) |_ \n" +
		" | ' \\ / _ \\(_-< |  _| |  _|\n" +
		" |_||_|\\___//__/  \\__|_|\\__| " + cursor + "\n" +
		"\n" +
		"  You are inside the container of your hostit app \"" + self.Name + "\".\n" +
		"  It is yours alone: your own filesystem, processes and ports.\n" +
		"\n" +
		"  App:    " + self.Name + "\n" +
		"  URL:    " + self.URL + "\n" +
		"  Files:  " + workspace.ContainerHome + " (upload with scp/rsync, or via the REST API)\n" +
		"\n" +
		"  Configure the app in hostit.yml (mode: static, or mode: app with run:), then:\n" +
		"\n" +
		"    hostit deploy      apply hostit.yml and (re)start the app\n" +
		"    hostit start/stop  start or stop the app (container stays up)\n" +
		"    hostit restart     restart the app (fast)\n" +
		"    hostit poweroff    stop the container; poweron/reboot to bring it back\n" +
		"    hostit status      is it running?\n" +
		"    hostit logs -f     watch its output\n" +
		"\n" +
		"  \"hostit guide\" explains the rest (where files go, what is installed).\n" +
		"  README.md is this app's own file: what it is, and what you changed.\n" +
		"\n"
}

func execAgent(_ *cli.Context) error {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	return agent.New(home).Run()
}

// isTerminal reports whether the given file is a terminal: it decides both
// whether podman gets a TTY and whether a session is human enough for a banner
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
