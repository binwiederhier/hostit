package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"heckel.io/hostit/app"
)

func TestScopeStatesDropsAppsNotAskedAbout(t *testing.T) {
	t.Parallel()
	// A node may only report states for the apps control polled it about (the
	// apps it hosts). A compromised node returning an extra key for another
	// node's app must not reach the state cache.
	asked := []string{"mine1", "mine2"}
	reported := map[string]app.State{
		"mine1":  {AppState: "running"},
		"mine2":  {AppState: "stopped"},
		"victim": {AppState: "running"}, // an app on another node
	}
	scoped := scopeStates(reported, asked)
	assert.Len(t, scoped, 2)
	assert.Contains(t, scoped, "mine1")
	assert.Contains(t, scoped, "mine2")
	assert.NotContains(t, scoped, "victim", "a node cannot report state for an app it was not asked about")
}
