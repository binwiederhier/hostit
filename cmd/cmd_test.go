package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
)

func TestNewCommands(t *testing.T) {
	app := New()
	names := make([]string, 0)
	for _, c := range app.Commands {
		names = append(names, c.Name)
	}
	// On the host: drive apps over the API. The daemon moved to the
	// hostit-control binary (NewControl); the commands that act on "this app"
	// need a container to be in, so they are not offered here.
	for _, expected := range []string{"apps"} {
		assert.Contains(t, names, expected)
	}
	assert.NotContains(t, names, "serve", "the daemon is hostit-control's job now")
	control := NewControl()
	require.NotNil(t, control.Command("serve"))
	for _, hidden := range []string{"deploy", "start", "restart", "poweroff", "status", "logs", "guide"} {
		assert.NotContains(t, names, hidden, "%q cannot work outside a container", hidden)
	}
	apps := app.Command("apps")
	require.NotNil(t, apps)
	subNames := make([]string, 0)
	for _, c := range apps.Subcommands {
		subNames = append(subNames, c.Name)
	}
	// Everything the app's own page offers, from any machine with a token: the
	// CLI could create and delete apps but not start, stop or look at one
	for _, expected := range []string{"add", "list", "remove", "keys", "deploy", "start", "stop", "restart", "logs", "run"} {
		assert.Contains(t, subNames, expected)
	}
}

func TestLoginBanner(t *testing.T) {
	t.Parallel()
	banner := loginBanner(&appctl.SelfInfo{Name: "blog", URL: "https://blog.apps.example.com", Port: 10001})
	// The wordmark, with the same accent-coloured cursor as the web app's logo
	assert.Contains(t, banner, `|_||_|`)
	assert.Contains(t, banner, "\x1b[48;2;21;156;176m") // accent (#159cb0) background block
	assert.Contains(t, banner, "\x1b[0m")
	// Where am I, and what is this?
	assert.Contains(t, banner, "blog")
	assert.Contains(t, banner, "https://blog.apps.example.com")
	// The port is no longer shown: the app is told to listen on $PORT, and a
	// number the owner cannot usefully act on was just noise.
	assert.NotContains(t, banner, "10001")
	// What can I do here?
	for _, want := range []string{"hostit deploy", "hostit logs", "hostit status", "hostit.yml"} {
		assert.Contains(t, banner, want, "the banner must mention %q", want)
	}
	assert.True(t, strings.HasSuffix(banner, "\n"))
}

func TestGuideText(t *testing.T) {
	t.Parallel()
	guide := guideText(&appctl.SelfInfo{Name: "blog", URL: "https://blog.apps.example.com", Port: 10001})
	// Everything someone needs after their first ssh, and nothing that points at
	// a file we no longer write
	for _, want := range []string{
		"blog", "https://blog.apps.example.com", "10001",
		appctl.PublicDir + "/", appctl.BinDir + "/", appctl.LogDir + "/", appctl.SrcDir + "/",
		"hostit deploy", "hostit logs", "apt-get install", "NEVER at this home directory",
		"https://apps.example.com/docs",
	} {
		assert.Contains(t, guide, want)
	}
	assert.NotContains(t, guide, "HOSTIT.txt")
	// No line runs past a narrow terminal
	for _, line := range strings.Split(guide, "\n") {
		assert.LessOrEqual(t, len(line), 80, "line too long: %q", line)
	}
}

func TestDocsURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://apps.example.com/docs", docsURL("https://blog.apps.example.com"))
	assert.Equal(t, "the hostit web app", docsURL("http://localhost:2586"))
}

func TestCommandsInsideAContainerAreTheAppsOwn(t *testing.T) {
	// An SSH session arrives through "podman exec", which does not inherit the
	// container's environment, so this must not be the only signal
	t.Setenv("container", "podman")
	names := make([]string, 0)
	for _, c := range New().Commands {
		names = append(names, c.Name)
	}
	// An app's owner manages their app; they do not run the platform, and being
	// shown commands they cannot use only invites confusion
	for _, expected := range []string{"deploy", "start", "stop", "restart", "poweron", "poweroff", "reboot", "status", "logs", "guide"} {
		assert.Contains(t, names, expected)
	}
	for _, hidden := range []string{"serve", "admin", "shell", "enter"} {
		assert.NotContains(t, names, hidden, "%q must not exist inside a container", hidden)
	}
	// The agent is PID 1 in there, so it has to stay reachable
	assert.Contains(t, names, "agent")
}

func TestActionMessages(t *testing.T) {
	t.Parallel()
	// "snake stoped" and "snake restared" is what deriving English from the verb
	// gets you. App verbs act on the run: command, power verbs on the container.
	assert.Equal(t, "blog: app started", actionMessage("start", "blog"))
	assert.Equal(t, "blog: app stopped", actionMessage("stop", "blog"))
	assert.Equal(t, "blog: app restarted", actionMessage("restart", "blog"))
	assert.Equal(t, "blog: powered on", actionMessage("poweron", "blog"))
	assert.Equal(t, "blog: powered off", actionMessage("poweroff", "blog"))
	assert.Equal(t, "blog: rebooted", actionMessage("reboot", "blog"))
}
