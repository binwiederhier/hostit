package cmd

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
	"heckel.io/hostit/appctl"
)

const (
	// staticReadHeaderTimeout bounds header reads of the static file server
	staticReadHeaderTimeout = 10 * time.Second
)

var (
	cmdUp = &cli.Command{
		Name:   "up",
		Usage:  "(Re)deploy and start the app from hostit.yml",
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
	cmdStatic = &cli.Command{
		Name:  "static",
		Usage: "Serve a directory over HTTP (used by \"mode: static\" apps)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "dir", Aliases: []string{"d"}, Value: ".", Usage: "directory to serve"},
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

func execUp(_ *cli.Context) error {
	ctl := newController()
	msg, err := ctl.Up()
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

func execDown(_ *cli.Context) error {
	msg, err := newController().Down()
	if err != nil {
		return err
	}
	fmt.Println("App " + msg + ".")
	return nil
}

func execRestart(_ *cli.Context) error {
	msg, err := newController().Restart()
	if err != nil {
		return err
	}
	fmt.Println("App " + msg + ".")
	return nil
}

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
	logFile := filepath.Join(os.Getenv("HOME"), appctl.AppLogFile)
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
	dir := c.String("dir")
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
	return appctl.NewController(appctl.DefaultSocketFile())
}
