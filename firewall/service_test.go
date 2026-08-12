package firewall

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderRuleset(t *testing.T) {
	t.Parallel()
	out := renderRuleset([]Rule{{Port: 10000, UID: 200000}, {Port: 10001, UID: 265536}})

	// The table is replaced atomically (create + flush) with an accept-by-default
	// output chain.
	assert.Contains(t, out, "add table inet hostit")
	assert.Contains(t, out, "flush table inet hostit")
	assert.Contains(t, out, "policy accept")

	// Each port gets a v4 and v6 drop rule allowing only root (0) and the app's uid.
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10000 meta skuid != { 0, 200000 } counter drop")
	assert.Contains(t, out, "ip6 daddr ::1 tcp dport 10000 meta skuid != { 0, 200000 } counter drop")
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10001 meta skuid != { 0, 265536 } counter drop")
	assert.Contains(t, out, "ip6 daddr ::1 tcp dport 10001 meta skuid != { 0, 265536 } counter drop")
}

func TestRenderRulesetEmpty(t *testing.T) {
	t.Parallel()
	// No apps: still replaces the table (so stale rules are flushed), with no drops.
	out := renderRuleset(nil)
	assert.Contains(t, out, "flush table inet hostit")
	assert.NotContains(t, out, "drop")
	// One line each for table/flush/chain.
	assert.Equal(t, 3, strings.Count(out, "\n"))
}
