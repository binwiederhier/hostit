package cmd

import (
	"fmt"
	"heckel.io/hostit/app"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"syscall"

	"github.com/urfave/cli/v2"
)

const (
	// containerPrefix must match app.containerPrefix. This file deliberately
	// imports as little as possible: it is the only code that runs as root on
	// behalf of an app user, so its blast radius is kept small.
	containerPrefix = "hostit-app-"
)

var (
	// appUserRegex re-validates the resolved account name before it is passed to
	// podman, so a surprising name cannot turn into something argument-shaped
	appUserRegex = regexp.MustCompile(app.AppNamePattern)
	// termRegex keeps TERM to boring terminal names
	termRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,32}$`)

	// cmdEnter is the privileged half of "hostit shell": it runs as root through
	// a narrow sudoers grant and execs the caller into THEIR OWN container. The
	// target container is derived from SUDO_UID, never from arguments; the
	// caller's arguments only ever become the command run inside that container.
	//
	// Usage (from hostit-shell): hostit enter <TERM|-> [-c <command>]
	cmdEnter = &cli.Command{
		Name:            "enter",
		Hidden:          true,
		SkipFlagParsing: true,
		Action:          execEnter,
	}
)

func execEnter(c *cli.Context) error {
	if os.Geteuid() != 0 {
		return cli.Exit("hostit enter must run as root (via sudo)", 1)
	}
	// Identify the caller from the sudo environment, never from arguments
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil || uid == 0 {
		return cli.Exit("hostit enter must be called through sudo by an app user", 1)
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return cli.Exit("cannot resolve the calling user", 1)
	}
	if !appUserRegex.MatchString(u.Username) {
		return cli.Exit("not an app user", 1)
	}

	// Build the podman argv ourselves. The caller contributes only TERM and a
	// single command string, both passed as individual arguments (never through
	// a shell on this side).
	args := []string{"podman", "exec", "--interactive"}
	if isTerminal(os.Stdin) {
		args = append(args, "--tty")
	}
	if term := c.Args().Get(0); termRegex.MatchString(term) {
		args = append(args, "--env", "TERM="+term)
	}
	args = append(args, containerPrefix+u.Username)
	if c.NArg() >= 3 && c.Args().Get(1) == "-c" {
		args = append(args, "/bin/sh", "-lc", c.Args().Get(2))
	} else {
		args = append(args, "/bin/sh", "-lc", "command -v bash >/dev/null && exec bash -l; exec sh -l")
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		return fmt.Errorf("podman not found: %w", err)
	}
	return syscall.Exec(podman, args, minimalEnv())
}

// minimalEnv is the environment podman runs with; the caller's environment is
// deliberately not inherited into this privileged exec
func minimalEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
}
