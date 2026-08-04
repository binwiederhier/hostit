package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
// the same green cursor as the web app's logo), which app this is, and the
// handful of commands that do anything here.
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
		"  Files:  " + homeDir() + " (upload with scp/rsync, or via the REST API)\n" +
		"  Port:   listen on 0.0.0.0:$PORT (" + strconv.Itoa(self.Port) + ") -- nothing else is reachable\n" +
		"\n" +
		"  Configure the app in hostit.yml (static:, run: or image:), then:\n" +
		"\n" +
		"    hostit up          apply hostit.yml and (re)start the app\n" +
		"    hostit down        stop the app\n" +
		"    hostit restart     restart it\n" +
		"    hostit status      is it running?\n" +
		"    hostit logs -f     watch its output\n" +
		"\n" +
		"  See HOSTIT.txt for the full story; README.md is this app's own notes.\n" +
		"\n"
}

// cursor is the wordmark's blinking block, drawn as a reverse-video space so
// the source stays ASCII: green foreground reversed renders as a green block
const cursor = "\x1b[32m\x1b[7m \x1b[0m"

// homeDir is where the app's files live inside the container
func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	return "your home directory"
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
