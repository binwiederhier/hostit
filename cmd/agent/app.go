package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/config"
)

const (
	// staticReadHeaderTimeout bounds header reads of the static file server
	staticReadHeaderTimeout = 10 * time.Second
)

var (
	cmdDeploy = &cli.Command{
		Name:   "deploy",
		Usage:  "Apply hostit.yml and (re)start the app",
		Action: execDeploy,
	}
	cmdStart = &cli.Command{
		Name:   "start",
		Usage:  "Start the app's run: command (inside a running container)",
		Action: execStart,
	}
	cmdStop = &cli.Command{
		Name:   "stop",
		Usage:  "Stop the app's run: command, leaving the container running",
		Action: execStop,
	}
	cmdRestart = &cli.Command{
		Name:   "restart",
		Usage:  "Restart the app's run: command (fast; no container recreate)",
		Action: execRestart,
	}
	cmdPowerOn = &cli.Command{
		Name:   "poweron",
		Usage:  "Start the app's container",
		Action: execPowerOn,
	}
	cmdPowerOff = &cli.Command{
		Name:   "poweroff",
		Usage:  "Stop the app's container, and keep it off across reboots",
		Action: execPowerOff,
	}
	cmdReboot = &cli.Command{
		Name:   "reboot",
		Usage:  "Reboot the app's container",
		Action: execReboot,
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
	cmdStatic = &cli.Command{
		Name:  "static",
		Usage: "Serve the app's public/ directory over HTTP (used by \"mode: static\" apps)",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "port", Aliases: []string{"p"}, Usage: "port to listen on; defaults to $PORT"},
		},
		Action: execStatic,
	}
	cmdInfo = &cli.Command{
		Name:   "info",
		Usage:  "Show the app's name, URL and port",
		Action: execInfo,
	}
)

func execDeploy(_ *cli.Context) error {
	ctl := newController()
	msg, err := ctl.Deploy()
	if err != nil {
		return err
	}
	self, err := ctl.Self()
	if err != nil {
		return err
	}
	fmt.Printf("App %s: %s\n", self.Name, msg)
	fmt.Printf("URL: %s\n", self.URL)
	fmt.Println("Check \"hostit status\" and \"hostit logs\" if the app does not respond.")
	return nil
}

// lifecycleCmd runs one lifecycle action over the socket and prints its message
func lifecycleCmd(action func() (string, error)) error {
	msg, err := action()
	if err != nil {
		return err
	}
	fmt.Println("App " + msg + ".")
	return nil
}

func execStart(_ *cli.Context) error    { return lifecycleCmd(newController().Start) }
func execStop(_ *cli.Context) error     { return lifecycleCmd(newController().Stop) }
func execRestart(_ *cli.Context) error  { return lifecycleCmd(newController().Restart) }
func execPowerOn(_ *cli.Context) error  { return lifecycleCmd(newController().PowerOn) }
func execPowerOff(_ *cli.Context) error { return lifecycleCmd(newController().PowerOff) }
func execReboot(_ *cli.Context) error   { return lifecycleCmd(newController().Reboot) }

func execStatus(_ *cli.Context) error {
	out, err := newController().Status()
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// execLogs prints (or follows) app logs. Following tails the agent's log file
// directly; a plain read goes through the daemon so it works from anywhere.
func execLogs(c *cli.Context) error {
	logFile := filepath.Join(os.Getenv("HOME"), app.LogFile)
	if c.Bool("follow") {
		if _, err := os.Stat(logFile); err != nil {
			return errors.New("cannot follow: no agent log file yet; try \"hostit logs\" without -f")
		}
		tail := exec.Command("tail", "-n", fmt.Sprintf("%d", c.Int("lines")), "-F", logFile)
		tail.Stdin, tail.Stdout, tail.Stderr = os.Stdin, os.Stdout, os.Stderr
		return tail.Run()
	}
	out, err := newController().Logs(c.Int("lines"))
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func execInfo(_ *cli.Context) error {
	self, err := newController().Self()
	if err != nil {
		return err
	}
	fmt.Printf("App:  %s\nURL:  %s\nPort: %d (a \"run:\" app must listen on 0.0.0.0:$PORT inside the container)\n", self.Name, self.URL, self.Port)
	return nil
}

// execStatic serves a directory; this is what a "mode: static" app runs, so a
// plain HTML app needs no runtime of its own
func execStatic(c *cli.Context) error {
	port := c.Int("port")
	if port == 0 {
		port, _ = strconv.Atoi(os.Getenv("PORT"))
	}
	if port == 0 {
		return errors.New("no port: pass --port or set $PORT")
	}
	// Always serve ~/public: a static app has exactly one place for its web
	// files, so there is nothing to point at and nothing to get wrong.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve home directory: %w", err)
	}
	dir := filepath.Join(home, app.PublicDir)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("cannot serve %s: %w", dir, err)
	}
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Printf("Serving %s on %s\n", dir, addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           appctl.StaticHandler(dir),
		ReadHeaderTimeout: staticReadHeaderTimeout,
	}
	return server.ListenAndServe()
}

func newController() *appctl.Controller {
	return appctl.NewController(config.DefaultSocketFile)
}
