package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"heckel.io/hostit/appctl"
)

// The front door dispatches; it does not own app or daemon commands itself.
// What tenants type inside a container comes from hostit-app (package appcli),
// mounted in as /usr/bin/hostit -- this binary never runs there.
func TestFrontDoorCommands(t *testing.T) {
	t.Parallel()
	names := make([]string, 0)
	for _, c := range New("v0.0.0-test").Commands {
		names = append(names, c.Name)
	}
	for _, expected := range []string{"control", "node", "proxy", "apps", "shell", "enter"} {
		assert.Contains(t, names, expected)
	}
	for _, gone := range []string{"serve", "deploy", "start", "logs", "status", "mcp", "agent"} {
		assert.NotContains(t, names, gone, "%q belongs to a component or to hostit-app", gone)
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
