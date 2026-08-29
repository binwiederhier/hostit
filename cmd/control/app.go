package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/control/client"
	"heckel.io/hostit/control/config"
)

var (
	// Named for what it manages, not for who may use it: an account token drives
	// its owner's apps through exactly these commands, and calling that "admin"
	// suggested a privilege it never required.
	cmdApp = &cli.Command{
		Name:    "app",
		Aliases: []string{"apps"}, // v0.17.0 shipped the plural; scripts may say it
		Usage:   "Manage apps on a hostit server via its REST API",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Aliases: []string{"H"}, EnvVars: []string{"HOSTIT_HOST"}, Usage: "remote API base URL, e.g. https://hostit.apps.example.com (default: the local unix socket)"},
			&cli.StringFlag{Name: "token", Aliases: []string{"t"}, EnvVars: []string{"HOSTIT_TOKEN"}, Usage: "account or admin API token (needed with --host)"},
		},
		Subcommands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "Create a new app (subdomain + SSH login)",
				ArgsUsage: "<name>",
				Action:    execAppsAdd,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "ssh-key", Aliases: []string{"k"}, Usage: "authorized SSH public key (literal or path to .pub file); repeatable"},
					&cli.BoolFlag{Name: "json", Usage: "print raw JSON response"},
				},
			},
			{
				Name:   "list",
				Usage:  "List all apps",
				Action: execAppsList,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "print raw JSON response"},
				},
			},
			{
				Name:      "remove",
				Usage:     "Delete an app, its Unix user and ALL its data",
				ArgsUsage: "<name>",
				Action:    execAppsRemove,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "force", Aliases: []string{"yes", "y"}, Usage: "do not ask for confirmation"},
				},
			},
			{
				Name:      "keys",
				Usage:     "Replace an app's authorized SSH keys",
				ArgsUsage: "<name>",
				Action:    execAppsKeys,
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "ssh-key", Aliases: []string{"k"}, Usage: "authorized SSH public key (literal or path to .pub file); repeatable"},
				},
			},
			{
				Name:      "deploy",
				Usage:     "Apply the app's hostit.yml and (re)start it",
				ArgsUsage: "<name>",
				Action:    execRemoteAction("deploy"),
			},
			{
				Name:      "start",
				Usage:     "Start the app's run: command (inside a running container)",
				ArgsUsage: "<name>",
				Action:    execRemoteAction("start"),
			},
			{
				Name:      "stop",
				Usage:     "Stop the app's run: command, leaving the container running",
				ArgsUsage: "<name>",
				Action:    execRemoteAction("stop"),
			},
			{
				Name:      "restart",
				Usage:     "Restart the app's run: command (fast; no container recreate)",
				ArgsUsage: "<name>",
				Action:    execRemoteAction("restart"),
			},
			{
				Name:  "power",
				Usage: "Power the app's container on or off, or reboot it",
				Subcommands: []*cli.Command{
					{Name: "on", Usage: "Start the app's container", ArgsUsage: "<name>", Action: execRemoteAction("poweron")},
					{Name: "off", Usage: "Stop the app's container, and keep it off across reboots", ArgsUsage: "<name>", Action: execRemoteAction("poweroff")},
					{Name: "reboot", Usage: "Reboot the app's container", ArgsUsage: "<name>", Action: execRemoteAction("reboot")},
				},
			},
			{
				Name:  "visibility",
				Usage: "Publish the app to the world, or restrict it to its owner and collaborators",
				Subcommands: []*cli.Command{
					{Name: "public", Usage: "Anyone with the URL can open the app", ArgsUsage: "<name>", Action: execSetVisibility(false)},
					{Name: "private", Usage: "Only the owner, its collaborators and admins can open the app", ArgsUsage: "<name>", Action: execSetVisibility(true)},
				},
			},
			{
				Name:  "snapshot",
				Usage: "Manage the app's snapshots (needs a btrfs host)",
				Subcommands: []*cli.Command{
					{Name: "list", Usage: "List the app's snapshots", ArgsUsage: "<name>", Action: execSnapshots},
					{Name: "create", Usage: "Take a snapshot of the app now, optionally labelled", ArgsUsage: "<name> [label]", Action: execSnapshot},
					{Name: "delete", Usage: "Delete one of the app's snapshots", ArgsUsage: "<name> <snapshot-id>", Action: execRemoveSnapshot},
				},
			},
			{
				Name:      "rollback",
				Usage:     "Roll the app back to a snapshot (a safety snapshot is taken first)",
				ArgsUsage: "<name> <snapshot-id>",
				Action:    execRollback,
			},
			{
				Name:      "fork",
				Usage:     "Duplicate an app into a new one (seeds from the source, or a snapshot; needs a btrfs host)",
				ArgsUsage: "<source> <new-name> [snapshot-id]",
				Action:    execFork,
			},
			{
				Name:  "domain",
				Usage: "Manage an app's custom domains",
				Subcommands: []*cli.Command{
					{Name: "list", Usage: "List an app's custom domains", ArgsUsage: "<app>", Action: execDomainList},
					{Name: "add", Usage: "Attach a custom domain (prints the DNS records to create)", ArgsUsage: "<app> <domain>", Action: execDomainAdd},
					{Name: "verify", Usage: "Re-check DNS and (re)issue the certificate", ArgsUsage: "<app> <domain>", Action: execDomainVerify},
					{Name: "rm", Usage: "Detach a custom domain", ArgsUsage: "<app> <domain>", Action: execDomainRemove},
				},
			},
			{
				Name:      "logs",
				Usage:     "Show the app's recent output",
				ArgsUsage: "<name>",
				Action:    execRemoteLogs,
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "lines", Aliases: []string{"n"}, Value: 100, Usage: "number of lines to show"},
				},
			},
			{
				Name:      "run",
				Usage:     "Run one shell command inside the app's container",
				ArgsUsage: "<name> <command>",
				Action:    execRemoteRun,
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "timeout", Usage: "seconds to allow (the server caps this)"},
				},
			},
		},
	}
)

func execAppsAdd(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit control app add <name>")
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
	fmt.Printf("\nThen: upload your app, edit hostit.yml, and run \"hostit deploy\" (\"hostit guide\" explains more).\n")
	return nil
}

// execRemoteAction runs one lifecycle verb against a remote app
// execSetVisibility flips one app between public and private. Two verbs rather
// than a --private flag, so the command reads as the state it leaves behind.
func execSetVisibility(private bool) cli.ActionFunc {
	return func(c *cli.Context) error {
		cl, err := appsClient(c)
		if err != nil {
			return err
		}
		word := "public"
		if private {
			word = "private"
		}
		if c.NArg() != 1 {
			return fmt.Errorf("usage: hostit control app visibility %s <name>", word)
		}
		name := c.Args().First()
		if err := cl.SetVisibility(name, private); err != nil {
			return err
		}
		fmt.Fprintf(c.App.Writer, "App %s is now %s\n", name, word)
		return nil
	}
}

func execRemoteAction(verb string) cli.ActionFunc {
	return func(c *cli.Context) error {
		cl, err := appsClient(c)
		if err != nil {
			return err
		}
		if c.NArg() != 1 {
			return fmt.Errorf("usage: hostit control app %s <name>", verb)
		}
		name := c.Args().First()
		switch verb {
		case "deploy":
			msg, err := cl.Deploy(name)
			if err != nil {
				return err
			}
			fmt.Println(msg)
		case "start":
			err = cl.Start(name)
		case "stop":
			err = cl.Stop(name)
		case "restart":
			err = cl.Restart(name)
		case "poweron":
			err = cl.PowerOn(name)
		case "poweroff":
			err = cl.PowerOff(name)
		case "reboot":
			err = cl.Reboot(name)
		}
		if err != nil {
			return err
		}
		if verb != "deploy" {
			fmt.Println(actionMessage(verb, name))
		}
		return nil
	}
}

// actionMessage says what happened, in words rather than derived from the verb
func actionMessage(verb, name string) string {
	switch verb {
	case "start":
		return name + ": app started"
	case "stop":
		return name + ": app stopped"
	case "restart":
		return name + ": app restarted"
	case "poweron":
		return name + ": powered on"
	case "poweroff":
		return name + ": powered off"
	case "reboot":
		return name + ": rebooted"
	}
	return name + ": " + verb
}

func execRemoteLogs(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit control app logs [-n <lines>] <name>  (flags come before the name)")
	}
	out, err := cl.Logs(c.Args().First(), c.Int("lines"))
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func execRemoteRun(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() < 2 {
		return errors.New(`usage: hostit control app run <name> "<command>"`)
	}
	res, err := cl.Run(c.Args().First(), strings.Join(c.Args().Slice()[1:], " "), c.Int("timeout"))
	if err != nil {
		return err
	}
	fmt.Print(res.Output)
	if res.TimedOut {
		fmt.Fprintln(os.Stderr, "hostit: the command ran out of time and was stopped")
	}
	if res.ExitCode != 0 {
		return cli.Exit("", res.ExitCode)
	}
	return nil
}

func execAppsList(c *cli.Context) error {
	cl, err := appsClient(c)
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
	rows := make([][]string, 0, len(apps))
	for _, app := range apps {
		rows = append(rows, []string{app.Name, app.ID, app.URL, strconv.Itoa(app.Port), app.CreatedAt.Format("2006-01-02")})
	}
	fmt.Println(util.Render([]string{"NAME", "ID", "URL", "PORT", "CREATED"}, rows))
	return nil
}

func execAppsRemove(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit control app remove <name>")
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

func execAppsKeys(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 || len(c.StringSlice("ssh-key")) == 0 {
		return errors.New("usage: hostit control app keys --ssh-key <key-or-file> [--ssh-key ...] <name>  (flags come before the name)")
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

// transport is how the CLI reaches a daemon: a remote REST API or the local
// unix socket, on which the peer credentials make root the global admin
type transport int

const (
	transportRemote transport = iota
	transportSocket
)

func appsClient(c *cli.Context) (*client.Client, error) {
	host, token := c.String("host"), c.String("token")
	socketFile := localSocketFile()
	_, statErr := os.Stat(socketFile)
	tr, err := resolveTransport(host, token, socketFile, statErr == nil)
	if err != nil {
		return nil, err
	}
	if tr == transportRemote {
		return client.New(host, token), nil
	}
	return client.NewSocket(socketFile), nil
}

// resolveTransport picks the way to the daemon: an explicit --host means remote
// (and needs a token); otherwise the local unix socket, whose absence means
// there is no daemon to talk to
func resolveTransport(host, token, socketFile string, socketExists bool) (transport, error) {
	if host != "" {
		if token == "" {
			return 0, errors.New("--token is required with --host (or set HOSTIT_TOKEN)")
		}
		return transportRemote, nil
	}
	if !socketExists {
		return 0, fmt.Errorf("hostit daemon socket not found at %s; is the daemon running? For a remote daemon, pass --host and --token.", socketFile)
	}
	return transportSocket, nil
}

// localSocketFile is CONTROL's socket: operator commands are control-plane
// operations, and the app socket (the node's) refuses them since the relay
// split. Control's config names a non-default path when there is one; a plain
// operator may not be able to read that file, so failures fall back to the
// built-in default rather than erroring.
func localSocketFile() string {
	if conf, err := config.LoadConfig(config.DefaultControlConfigFile); err == nil && conf.ControlSocketFile != "" {
		return conf.ControlSocketFile
	}
	return config.DefaultControlSocketFile
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
