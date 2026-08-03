package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
