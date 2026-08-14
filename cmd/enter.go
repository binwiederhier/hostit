package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"syscall"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/container"
)

const (
	// containerPrefix must match workspace.ContainerPrefix. This file deliberately
	// imports as little as possible: it is the only code that runs as root on
	// behalf of an app user, so its blast radius is kept small.
	containerPrefix = "hostit-app-"
)

var (
	// termRegex keeps TERM to boring terminal names. The app-name and container-key
	// formats are re-validated via app.ValidName / container.ValidName, which own
	// those patterns.
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
	if !app.ValidName(u.Username) {
		return cli.Exit("not an app user", 1)
	}
	// Resolve the app's container from the caller's home directory, not its name.
	// Containers are keyed on the app's stable id, and the app user's home lives
	// INSIDE the id-keyed app subvolume (apps/<id>/home/app), so the id comes out
	// of the home path. A rename never changes it. (A pre-unification home is
	// still apps/<id> itself, so this is correct across the migration.)
	// Everything here comes from the SUDO_UID user, never from args.
	containerKey, ok := containerKeyFromHome(u.HomeDir)
	if !ok {
		return cli.Exit("cannot resolve the app container", 1)
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
	args = append(args, containerKey)
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

// containerKeyFromHome turns an app user's home directory into the name of its
// container. The container is keyed on the app's id, which app.IDFromHomeDir
// digs out of the home path (apps/<id>/home/app, or the pre-unification
// apps/<id>); the "hostit-app-" prefix is added. It returns false for a home
// whose id is not a safe container key, so a surprising passwd entry cannot
// inject podman arguments.
func containerKeyFromHome(home string) (string, bool) {
	base := app.IDFromHomeDir(home)
	if !container.ValidName(base) {
		return "", false
	}
	return containerPrefix + base, true
}

// minimalEnv is the environment podman runs with; the caller's environment is
// deliberately not inherited into this privileged exec
func minimalEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
}
