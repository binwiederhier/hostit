package cmd

import (
	"fmt"
	"heckel.io/hostit/app"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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
	// containerKeyRegex re-validates the container key (an app id, or an app name
	// for a pre-id app) taken from the caller's home-dir path, before it reaches
	// podman -- same argument-shaped-input guard as appUserRegex
	containerKeyRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
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
	// Resolve the app's container from the caller's home directory, not its name.
	// Containers are keyed on the app's stable id, and the app user's home IS the
	// id-keyed path (apps/<id>), so its basename is the container key. A rename
	// never changes it. (A pre-id app's home is still apps/<name>, whose basename
	// matches that app's name-keyed container, so this is correct across the
	// migration.) Everything here comes from the SUDO_UID user, never from args.
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
// container. The container is keyed on the app's id, and the home is the id-keyed
// path (apps/<id>), so the basename is the key; the "hostit-app-" prefix is added.
// It returns false for a home whose basename is not a safe container key, so a
// surprising passwd entry cannot inject podman arguments.
func containerKeyFromHome(home string) (string, bool) {
	base := filepath.Base(filepath.Clean(home))
	if !containerKeyRegex.MatchString(base) {
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
