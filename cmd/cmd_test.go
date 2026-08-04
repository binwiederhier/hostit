package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
)

func TestNewCommands(t *testing.T) {
	t.Parallel()
	app := New()
	names := make([]string, 0)
	for _, c := range app.Commands {
		names = append(names, c.Name)
	}
	for _, expected := range []string{"serve", "up", "down", "restart", "status", "logs", "info", "admin"} {
		assert.Contains(t, names, expected)
	}
	admin := app.Command("admin")
	require.NotNil(t, admin)
	subNames := make([]string, 0)
	for _, c := range admin.Subcommands {
		subNames = append(subNames, c.Name)
	}
	for _, expected := range []string{"add", "list", "remove", "keys"} {
		assert.Contains(t, subNames, expected)
	}
}

func TestLoginBanner(t *testing.T) {
	t.Parallel()
	banner := loginBanner(&appctl.SelfInfo{Name: "blog", URL: "https://blog.apps.example.com", Port: 10001})
	// The wordmark, with the same green cursor as the web app's logo
	assert.Contains(t, banner, `|_||_|`)
	assert.Contains(t, banner, "\x1b[32m")
	assert.Contains(t, banner, "\x1b[0m")
	// Where am I, and what is this?
	assert.Contains(t, banner, "blog")
	assert.Contains(t, banner, "https://blog.apps.example.com")
	assert.Contains(t, banner, "10001")
	// What can I do here?
	for _, want := range []string{"hostit up", "hostit logs", "hostit status", "hostit.yml"} {
		assert.Contains(t, banner, want, "the banner must mention %q", want)
	}
	assert.True(t, strings.HasSuffix(banner, "\n"))
}

func TestAppUserRegexMatchesTheServerRule(t *testing.T) {
	t.Parallel()
	// Deliberately duplicated (cmd must not import the app package for this),
	// so the only thing that can keep the two honest is a test
	assert.Equal(t, app.AppNamePattern, appUserRegex.String(),
		"the enter helper's user check must match the rule the server creates apps by")
}
