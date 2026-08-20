package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/urfave/cli/v2"
)

// The hostit binary is the operator's front door: one command that dispatches
// to the component binaries (hostit-control, hostit-node, hostit-proxy), so
// nobody has to remember which daemon owns which subcommand -- and a component
// that is not installed says so, naming what this machine is.
//
// It is NOT what runs inside containers any more: that is hostit-app (package
// appcli), bind-mounted in as /usr/bin/hostit. What tenants type is unchanged;
// what the file on the host contains is not. The shell and enter commands stay
// here because /usr/lib/hostit/bin/hostit-shell execs "hostit shell" -- they
// are entry points sshd reaches through this binary, not commands anyone types.

// components are the dispatchable siblings, in help order.
var components = []string{"control", "node", "proxy"}

func New(version string) *cli.App {
	commands := []*cli.Command{cmdShell, cmdEnter, cmdInternal, cmdAppsAlias}
	for _, name := range components {
		commands = append(commands, dispatchCommand(name))
	}
	return &cli.App{
		Name:     "hostit",
		Version:  version,
		Usage:    "self-hosted mini-app platform: isolated apps with SSH access, subdomains and TLS",
		Commands: commands,
	}
}

// dispatchCommand execs the sibling binary with the remaining arguments, so
// `hostit control apps list` IS `hostit-control apps list`.
func dispatchCommand(name string) *cli.Command {
	return &cli.Command{
		Name:            name,
		Usage:           "Run a hostit-" + name + " command (execs /usr/bin/hostit-" + name + ")",
		SkipFlagParsing: true, // the sibling owns its flags; parsing them here would eat --config
		Action: func(c *cli.Context) error {
			return execSibling("hostit-"+name, c.Args().Slice())
		},
	}
}

// cmdAppsAlias keeps the old spelling working: `hostit apps ...` was the
// operator's app CLI before it moved onto hostit-control, where the registry
// is. Deprecated, not removed -- muscle memory and scripts get a release to
// catch up, and the notice tells them where to.
var cmdAppsAlias = &cli.Command{
	Name:            "apps",
	Usage:           "Deprecated: use `hostit control apps ...`",
	SkipFlagParsing: true,
	Hidden:          true,
	Action: func(c *cli.Context) error {
		fmt.Fprintln(os.Stderr, "note: `hostit apps` moved to `hostit control apps`; this alias will go away")
		return execSibling("hostit-control", append([]string{"apps"}, c.Args().Slice()...))
	},
}

// execSibling replaces this process with the component binary. A missing one
// answers with what IS installed here, so the error names the machine's role
// rather than just a path.
func execSibling(binary string, args []string) error {
	path, err := exec.LookPath(binary)
	if err != nil {
		installed := make([]string, 0, len(components))
		for _, name := range components {
			if _, err := exec.LookPath("hostit-" + name); err == nil {
				installed = append(installed, "hostit-"+name)
			}
		}
		if len(installed) == 0 {
			return errors.New(binary + " is not installed on this machine")
		}
		return fmt.Errorf("%s is not installed on this machine (installed: %v)", binary, installed)
	}
	argv := append([]string{path}, args...)
	return syscall.Exec(path, argv, os.Environ())
}
