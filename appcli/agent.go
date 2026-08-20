package appcli

import (
	"os"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/agent"
)

// cmdAgent is PID 1 inside workspace containers: it supervises the app's
// run command (see the agent package).
var cmdAgent = &cli.Command{
	Name:   "agent",
	Hidden: true,
	Action: execAgent,
}

func execAgent(_ *cli.Context) error {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	return agent.New(home).Run()
}
