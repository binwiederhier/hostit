// Package appcli is the in-container command set: what /usr/bin/hostit is
// INSIDE an app container. It is built as the hostit-app binary, which lives at
// /usr/lib/hostit/bin/hostit-app on the host (off $PATH, so it cannot be run
// there by accident) and is bind-mounted into every container as
// /usr/bin/hostit -- the host filename and the command tenants type are
// deliberately different things.
//
// Its entire outward dependency is the app socket. No TLS, no OAuth, no store,
// no podman: that is the point of the split -- the binary mounted where the
// tenant is root carries nothing but what the tenant may use anyway.
package appcli

import (
	"github.com/urfave/cli/v2"
)

// New is the container CLI.
func New(version string) *cli.App {
	return &cli.App{
		Name:     "hostit",
		Version:  version,
		Usage:    "manage this app: deploy it, restart it, read its logs",
		Commands: []*cli.Command{cmdDeploy, cmdStart, cmdStop, cmdRestart, cmdPowerOn, cmdPowerOff, cmdReboot, cmdStatus, cmdLogs, cmdInfo, cmdGuide, cmdStatic, cmdAgent, cmdMCP},
	}
}
