package cmd

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/appctl"
)

var (
	cmdUp = &cli.Command{
		Name:   "up",
		Usage:  "(Re)deploy and start the app in the current home directory",
		Action: execUp,
	}
	cmdDown = &cli.Command{
		Name:   "down",
		Usage:  "Stop the app and disable it at boot",
		Action: execDown,
	}
	cmdRestart = &cli.Command{
		Name:   "restart",
		Usage:  "Restart the app",
		Action: execRestart,
	}
	cmdStatus = &cli.Command{
		Name:   "status",
		Usage:  "Show the app's service status",
		Action: execStatus,
	}
	cmdLogs = &cli.Command{
		Name:   "logs",
		Usage:  "Show the app's logs",
		Action: execLogs,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "follow the log output"},
			&cli.IntFlag{Name: "lines", Aliases: []string{"n"}, Value: 100, Usage: "number of lines to show"},
		},
	}
	cmdInfo = &cli.Command{
		Name:   "info",
		Usage:  "Show the app's name, URL and port",
		Action: execInfo,
	}
)

func execUp(_ *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	if err := ctl.Up(); err != nil {
		return err
	}
	self, err := ctl.Self()
	if err != nil {
		return err
	}
	fmt.Printf("App %s deployed and started: %s\n", self.Name, self.URL)
	fmt.Println("Check \"hostit status\" and \"hostit logs\" if the app does not respond.")
	return nil
}

func execDown(_ *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	if err := ctl.Down(); err != nil {
		return err
	}
	fmt.Println("App stopped.")
	return nil
}

func execRestart(_ *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	if err := ctl.Restart(); err != nil {
		return err
	}
	fmt.Println("App restarted.")
	return nil
}

func execStatus(_ *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	out, err := ctl.Status()
	fmt.Print(out) // systemctl status exits non-zero for stopped units; still show output
	if out == "" && err != nil {
		return err
	}
	return nil
}

func execLogs(c *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	return ctl.Logs(c.Bool("follow"), c.Int("lines"))
}

func execInfo(_ *cli.Context) error {
	ctl, err := newController()
	if err != nil {
		return err
	}
	self, err := ctl.Self()
	if err != nil {
		return err
	}
	fmt.Printf("App:  %s\nURL:  %s\nPort: %d (your app must listen on 127.0.0.1:%d, see $PORT)\n", self.Name, self.URL, self.Port, self.Port)
	return nil
}

func newController() (*appctl.Controller, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return appctl.NewController(home, appctl.DefaultSocketFile(), appctl.NewRunner()), nil
}
