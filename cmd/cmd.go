// Package cmd wires up the hostit CLI: the daemon ("serve"), the app-side deploy
// commands ("deploy", "restart", ...) and the remote client ("apps ...").
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
	// Two tools that happen to share a binary. Inside a container the commands act
	// on this app through the daemon's socket, which identifies the caller by its
	// uid; on the host there is no app to be, so those commands cannot work and
	// listing them only invites the question.
	commands := []*cli.Command{cmdApps, cmdShell, cmdEnter, cmdInternal}
	usage := "self-hosted mini-app platform: isolated apps with SSH access, subdomains and TLS"
	if insideContainer() {
		commands = []*cli.Command{cmdDeploy, cmdStart, cmdStop, cmdRestart, cmdPowerOn, cmdPowerOff, cmdReboot, cmdStatus, cmdLogs, cmdInfo, cmdGuide, cmdStatic, cmdAgent, cmdMCP}
		usage = "manage this app: deploy it, restart it, read its logs"
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

// NewControl creates the hostit-control CLI: the control plane as its own
// binary and systemd service (the four-service split). The daemon that used
// to be "hostit serve" lives here; the hostit binary keeps the CLI and the
// in-container agent.
func NewControl() *cli.App {
	return &cli.App{
		Name:     "hostit-control",
		Usage:    "hostit's control plane: web app, REST API, registry, placement, certificates",
		Commands: []*cli.Command{cmdServe},
	}
}
