package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/node"
	"heckel.io/hostit/nodeconf"
)

// cmdStatus asks the RUNNING daemon over its root-only status socket: the
// point is the live link state, which no file on disk can answer.
var (
	cmdStatus = &cli.Command{
		Name:  "status",
		Usage: "Show this node: identity, control link, and the apps placed here",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: node.DefaultConfigFile, Usage: "node config file"},
			&cli.BoolFlag{Name: "json", Usage: "print the raw status as JSON"},
		},
		Action: execStatus,
	}
)

func execStatus(c *cli.Context) error {
	body, err := getSocket(statusSocketFile(c.String("config")), "/v1/status")
	if err != nil {
		return err
	}
	if c.Bool("json") {
		_, err := c.App.Writer.Write(body)
		return err
	}
	var s node.Status
	if err := json.Unmarshal(body, &s); err != nil {
		return err
	}
	renderNodeStatus(c.App.Writer, &s)
	return nil
}

func renderNodeStatus(w io.Writer, s *node.Status) {
	link := "NOT CONNECTED"
	if s.Connected {
		link = "connected"
	}
	fmt.Fprintln(w, util.Title("NODE "+s.NodeID))
	fmt.Fprintf(w, "  version  %s\n", s.Version)
	fmt.Fprintf(w, "  control  %s (%s)\n\n", s.ControlURL, link)
	fmt.Fprintln(w, util.Title(fmt.Sprintf("APPS (%d)", len(s.Apps))))
	if len(s.Apps) == 0 {
		fmt.Fprintln(w, "  none placed on this node")
		return
	}
	rows := make([][]string, 0, len(s.Apps))
	for _, a := range s.Apps {
		rows = append(rows, []string{a.Name, strconv.Itoa(a.UID), strconv.Itoa(a.Port)})
	}
	fmt.Fprintln(w, util.Render([]string{"NAME", "UID", "PORT"}, rows))
}

// statusSocketFile resolves the daemon's status socket from its config; an
// unreadable config falls back to the packaged default rather than erroring.
func statusSocketFile(configPath string) string {
	if conf, err := nodeconf.LoadConfig(configPath); err == nil && conf.NodeSocketFile != "" {
		return conf.NodeSocketFile
	}
	return nodeconf.DefaultNodeSocketFile
}

// getSocket does one GET against the daemon's unix socket, with an error that
// says "daemon not running" instead of a bare dial failure.
func getSocket(path, urlPath string) ([]byte, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("hostit-node does not appear to be running on this host (no socket at %s)", path)
	}
	client := http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", path)
			},
		},
	}
	resp, err := client.Get("http://hostit-node" + urlPath)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the daemon answered %s: %s", resp.Status, body)
	}
	return body, nil
}
