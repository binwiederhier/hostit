package appcli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
)

// The container CLI offers exactly the app's own commands: the platform is not
// its business, and PID 1's agent command has to stay reachable.
func TestContainerCommandsAreTheAppsOwn(t *testing.T) {
	t.Parallel()
	names := make([]string, 0)
	for _, c := range New("v0.0.0-test").Commands {
		names = append(names, c.Name)
	}
	for _, expected := range []string{"deploy", "start", "stop", "restart", "poweron", "poweroff", "reboot", "status", "logs", "guide", "agent", "mcp", "static", "info"} {
		assert.Contains(t, names, expected)
	}
	for _, hidden := range []string{"serve", "admin", "shell", "enter", "apps", "control", "node", "proxy"} {
		assert.NotContains(t, names, hidden, "%q must not exist inside a container", hidden)
	}
}

func TestGuideText(t *testing.T) {
	t.Parallel()
	guide := guideText(&appctl.SelfInfo{Name: "blog", URL: "https://blog.apps.example.com", Port: 10001})
	// Everything someone needs after their first ssh, and nothing that points at
	// a file we no longer write
	for _, want := range []string{
		"blog", "https://blog.apps.example.com", "10001",
		app.PublicDir + "/", app.BinDir + "/", app.LogDir + "/", app.SrcDir + "/",
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
