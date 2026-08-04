// Package cmd wires up the hostit CLI: the daemon ("serve"), the app-side deploy
// commands ("up", "down", ...) and the remote admin client ("admin ...").
package cmd

import (
	"os"

	"github.com/urfave/cli/v2"
)

// New creates the hostit CLI application.
//
// The same binary is bind-mounted into every app container, but an app's owner
// is not an operator of the host: inside a container they get their own app's
// commands and nothing else. Listing "serve" or "admin" there would advertise a
// surface they cannot use and should not have to think about -- they are refused
// anyway, since the daemon is already running and admin needs a token they do
// not have.
func New() *cli.App {
	commands := []*cli.Command{
		cmdUp,
		cmdDown,
		cmdRestart,
		cmdStatus,
		cmdLogs,
		cmdInfo,
		cmdGuide,
		cmdStatic,
		cmdAgent,
	}
	usage := "manage this app: deploy it, restart it, read its logs"
	if !insideContainer() {
		commands = append(commands, cmdServe, cmdAdmin, cmdShell, cmdEnter)
		usage = "self-hosted mini-app platform: isolated apps with SSH access, subdomains and TLS"
	}
	return &cli.App{
		Name:     "hostit",
		Usage:    usage,
		Commands: commands,
	}
}

// insideContainer reports whether this process runs inside an app container.
//
// The marker file, not the container= variable: podman sets that for the
// container's main process, but "podman exec" (how an SSH session gets in)
// starts with a fresh environment, so an app owner's shell would not have it.
func insideContainer() bool {
	for _, marker := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return os.Getenv("container") != ""
}
