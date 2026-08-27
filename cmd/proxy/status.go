package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/proxy"
	"heckel.io/hostit/proxy/api"
)

// The status and route commands ask the RUNNING daemon over its root-only
// status socket: they show what the proxy is actually serving from its cache,
// which is exactly what an operator wants when control and proxy disagree.
var (
	cmdStatus = &cli.Command{
		Name:  "status",
		Usage: "Show this proxy: identity, control link, and how much it is serving",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: proxy.DefaultConfigFile, Usage: "proxy config file"},
			&cli.BoolFlag{Name: "json", Usage: "print the raw status as JSON"},
		},
		Action: execStatus,
	}
	cmdRoute = &cli.Command{
		Name:  "route",
		Usage: "Inspect the routing table this proxy serves from",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the cached routes: hostname -> target",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: proxy.DefaultConfigFile, Usage: "proxy config file"},
					&cli.BoolFlag{Name: "json", Usage: "print the raw table as JSON"},
				},
				Action: execRouteList,
			},
		},
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
	var s proxy.Status
	if err := json.Unmarshal(body, &s); err != nil {
		return err
	}
	renderProxyStatus(c.App.Writer, &s)
	return nil
}

func execRouteList(c *cli.Context) error {
	body, err := getSocket(statusSocketFile(c.String("config")), "/v1/routes")
	if err != nil {
		return err
	}
	if c.Bool("json") {
		_, err := c.App.Writer.Write(body)
		return err
	}
	var table api.Table
	if err := json.Unmarshal(body, &table); err != nil {
		return err
	}
	renderRoutes(c.App.Writer, &table)
	return nil
}

func renderProxyStatus(w io.Writer, s *proxy.Status) {
	link := "NOT CONNECTED"
	if s.Connected {
		link = "connected"
	}
	fmt.Fprintln(w, util.Title("PROXY "+s.ProxyID))
	fmt.Fprintf(w, "  version  %s\n", s.Version)
	fmt.Fprintf(w, "  control  %s\n", s.ControlURL)
	fmt.Fprintf(w, "  cluster  %s (%s)\n", s.ClusterURL, link)
	fmt.Fprintf(w, "  serving  %d routes (table seq %d), %d certificates cached\n", s.Routes, s.TableSeq, s.CertsCached)
}

func renderRoutes(w io.Writer, table *api.Table) {
	fmt.Fprintln(w, util.Title(fmt.Sprintf("ROUTES (%d), table seq %d", len(table.Routes), table.Seq)))
	if len(table.Routes) == 0 {
		fmt.Fprintln(w, "  no routes cached; has this proxy ever reached control?")
		return
	}
	rows := make([][]string, 0, len(table.Routes))
	for _, route := range table.Routes {
		rows = append(rows, []string{route.Host, route.Target})
	}
	fmt.Fprintln(w, util.Render([]string{"HOST", "TARGET"}, rows))
}

// statusSocketFile resolves the daemon's status socket from its config; an
// unreadable config falls back to the packaged default rather than erroring.
func statusSocketFile(configPath string) string {
	if conf, err := proxy.LoadFileConfig(configPath); err == nil && conf.SocketFile != "" {
		return conf.SocketFile
	}
	return proxy.DefaultSocketFile
}

// getSocket does one GET against the daemon's unix socket, with an error that
// says "daemon not running" instead of a bare dial failure.
func getSocket(path, urlPath string) ([]byte, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("hostit-proxy does not appear to be running on this host (no socket at %s)", path)
	}
	client := http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", path)
			},
		},
	}
	resp, err := client.Get("http://hostit-proxy" + urlPath)
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
