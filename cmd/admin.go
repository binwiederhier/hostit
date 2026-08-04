package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/client"
)

var (
	cmdAdmin = &cli.Command{
		Name:  "admin",
		Usage: "Manage apps on a hostit server via its REST API",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Aliases: []string{"H"}, EnvVars: []string{"HOSTIT_HOST"}, Usage: "API base URL, e.g. https://hostit.apps.example.com"},
			&cli.StringFlag{Name: "token", Aliases: []string{"t"}, EnvVars: []string{"HOSTIT_TOKEN"}, Usage: "admin token"},
		},
		Subcommands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "Create a new app (subdomain + SSH login)",
				ArgsUsage: "<name>",
				Action:    execAdminAdd,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "ssh-key", Aliases: []string{"k"}, Usage: "authorized SSH public key (literal or path to .pub file); repeatable"},
					&cli.BoolFlag{Name: "json", Usage: "print raw JSON response"},
				},
			},
			{
				Name:   "list",
				Usage:  "List all apps",
				Action: execAdminList,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "print raw JSON response"},
				},
			},
			{
				Name:      "remove",
				Usage:     "Delete an app, its Unix user and ALL its data",
				ArgsUsage: "<name>",
				Action:    execAdminRemove,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "force", Usage: "do not ask for confirmation"},
				},
			},
			{
				Name:      "keys",
				Usage:     "Replace an app's authorized SSH keys",
				ArgsUsage: "<name>",
				Action:    execAdminKeys,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "ssh-key", Aliases: []string{"k"}, Usage: "authorized SSH public key (literal or path to .pub file); repeatable"},
				},
			},
		},
	}
)

func execAdminAdd(c *cli.Context) error {
	cl, err := adminClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit admin add <name>")
	}
	keys, err := readKeyFlags(c.StringSlice("ssh-key"))
	if err != nil {
		return err
	}
	app, err := cl.CreateApp(c.Args().First(), keys)
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printJSON(app)
	}
	fmt.Printf("App created!\n\n")
	fmt.Printf("  URL:  %s\n", app.URL)
	fmt.Printf("  SSH:  %s\n", app.SSH.Command)
	fmt.Printf("  Port: %d (the app must listen on 0.0.0.0:$PORT inside its container)\n", app.Port)
	fmt.Printf("\nThen: upload your app, edit hostit.yml, and run \"hostit up\" (\"hostit guide\" explains more).\n")
	return nil
}

func execAdminList(c *cli.Context) error {
	cl, err := adminClient(c)
	if err != nil {
		return err
	}
	apps, err := cl.Apps()
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printJSON(apps)
	}
	if len(apps) == 0 {
		fmt.Println("No apps.")
		return nil
	}
	for _, app := range apps {
		fmt.Printf("%-20s %-40s port %d, created %s\n", app.Name, app.URL, app.Port, app.CreatedAt.Format("2006-01-02"))
	}
	return nil
}

func execAdminRemove(c *cli.Context) error {
	cl, err := adminClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit admin remove <name>")
	}
	name := c.Args().First()
	if !c.Bool("force") {
		fmt.Printf("Really delete app %q, its user and ALL its data? Type the app name to confirm: ", name)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) != name {
			return errors.New("aborted")
		}
	}
	if err := cl.DeleteApp(name); err != nil {
		return err
	}
	fmt.Printf("App %s deleted.\n", name)
	return nil
}

func execAdminKeys(c *cli.Context) error {
	cl, err := adminClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 || len(c.StringSlice("ssh-key")) == 0 {
		return errors.New("usage: hostit admin keys <name> --ssh-key <key-or-file> [--ssh-key ...]")
	}
	keys, err := readKeyFlags(c.StringSlice("ssh-key"))
	if err != nil {
		return err
	}
	if err := cl.SetKeys(c.Args().First(), keys); err != nil {
		return err
	}
	fmt.Println("Keys updated.")
	return nil
}

func adminClient(c *cli.Context) (*client.Client, error) {
	host, token := c.String("host"), c.String("token")
	if host == "" || token == "" {
		return nil, errors.New("--host and --token are required (or set HOSTIT_HOST and HOSTIT_TOKEN)")
	}
	return client.New(host, token), nil
}

// readKeyFlags resolves --ssh-key values: file paths are read, literals passed through
func readKeyFlags(values []string) ([]string, error) {
	keys := make([]string, 0, len(values))
	for _, value := range values {
		if _, err := os.Stat(value); err == nil {
			b, err := os.ReadFile(value)
			if err != nil {
				return nil, err
			}
			keys = append(keys, strings.TrimSpace(string(b)))
		} else {
			keys = append(keys, strings.TrimSpace(value))
		}
	}
	return keys, nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
