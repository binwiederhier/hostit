package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	"github.com/urfave/cli/v2"
)

const (
	// relayRoutesFile maps a routed (remote) app to its node's SSH host. Written
	// by control, world-readable (it holds no secret), read to decide local vs
	// relay. relayKeyFile is the credential and stays root-only.
	relayRoutesFile     = "/var/lib/hostit/ssh-routes"
	relayHelperFile     = "/usr/bin/hostit-relay"
	relayKeyFile        = "/etc/hostit/relay_key"
	relayKnownHostsFile = "/etc/hostit/relay_known_hosts"
)

// cmdRelay is the privileged inner hop of the relay gateway. It is reached only
// as root via "sudo -n hostit-relay" from a routed app's login shell or sftp
// dispatcher; sshd passes the client's invocation straight through.
var cmdRelay = &cli.Command{
	Name:            "relay",
	Hidden:          true,
	SkipFlagParsing: true, // sshd's "-c <cmd>" / "-s sftp" must reach us intact
	Action:          execRelay,
}

func execRelay(c *cli.Context) error {
	// The app is derived from SUDO_UID (the login user), never from an argument,
	// so a tenant cannot ask to relay as a different app.
	app := appFromSudoUID()
	if app == "" {
		fmt.Fprintln(os.Stderr, "hostit: relay must be run via sudo from an app login")
		return cli.Exit("", 1)
	}
	backend := relayBackend(relayRoutesFile, app)
	if backend == "" {
		fmt.Fprintf(os.Stderr, "hostit: no relay route for app %q\n", app)
		return cli.Exit("", 1)
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found: %w", err)
	}
	args := relaySSHArgs(backend, app, c.Args().Slice())
	return syscall.Exec(sshBin, append([]string{sshBin}, args...), os.Environ())
}

// relaySSHArgs builds the inner ssh argv by sshd invocation: an interactive
// shell (no args) gets a tty; an exec/rsync/scp command ("-c <cmd>") is passed
// as the remote command; an sftp subsystem ("-s sftp") is requested as one.
func relaySSHArgs(backend, app string, invocation []string) []string {
	base := []string{
		"-i", relayKeyFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + relayKnownHostsFile,
		"-o", "LogLevel=ERROR",
	}
	target := app + "@" + backend
	switch {
	case len(invocation) == 0:
		return append(base, "-tt", target)
	case invocation[0] == "-c" && len(invocation) >= 2:
		return append(base, target, invocation[1])
	case invocation[0] == "-s" && len(invocation) >= 2:
		return append(base, "-s", target, invocation[1])
	default:
		return append(base, target, strings.Join(invocation, " "))
	}
}

// appFromSudoUID resolves the app from SUDO_UID (set by sudo to the login user's
// uid); the tenant cannot forge it to another app's uid.
func appFromSudoUID() string {
	uid := os.Getenv("SUDO_UID")
	if uid == "" {
		return ""
	}
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return u.Username
}

// relayBackend returns the node host a routed app relays to, or "" when the app
// has no route (it is local, or the relay is off).
func relayBackend(routesFile, app string) string {
	f, err := os.Open(routesFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, host, ok := strings.Cut(strings.TrimSpace(sc.Text()), "\t")
		if ok && name == app {
			return host
		}
	}
	return ""
}
