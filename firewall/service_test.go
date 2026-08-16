package firewall

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderRuleset(t *testing.T) {
	t.Parallel()
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 200000}, {Port: 10001, UID: 265536}}, "", nil)

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
	out := renderRuleset("hostit", nil, "", nil)
	assert.Contains(t, out, "flush table inet hostit")
	assert.NotContains(t, out, "drop")
	// One line each for table/flush/chain.
	assert.Equal(t, 3, strings.Count(out, "\n"))
}

func TestRulesetUsesTheNodeTable(t *testing.T) {
	t.Parallel()
	// Two colocated nodes must own separate tables, or each reconcile wipes
	// the other node's rules.
	out := renderRuleset("hostit_stage_node_2", []Rule{{Port: 10000, UID: 200000}}, "", nil)
	assert.Contains(t, out, "add table inet hostit_stage_node_2")
	assert.NotContains(t, out, "inet hostit ")
}

// An app published off loopback is reachable over the network, so the ruleset
// must say who may reach it: the control plane's addresses, nobody else.
// Without this, publishing on a node's VPC address would expose every app's
// port to anything that can route to the node -- including its public
// interface.
func TestRenderRulesetGuardsPortsPublishedOffLoopback(t *testing.T) {
	rules := []Rule{{Port: 10000, UID: 1000000}}
	out := renderRuleset("hostit_worker", rules, "10.111.32.4", []string{"10.111.32.3"})

	assert.Contains(t, out, "add chain inet hostit_worker input", "an input chain exists once ports leave loopback")
	assert.Contains(t, out, "ip saddr 10.111.32.3 tcp dport 10000 counter accept", "the control plane may reach the app")
	assert.Contains(t, out, "ip daddr 10.111.32.4 tcp dport 10000 counter drop", "everyone else is dropped")
	// The loopback isolation between apps on the same node is unchanged.
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10000 meta skuid != { 0, 1000000 } counter drop")
}

// A colocated node publishes on loopback, so there is nothing to guard and no
// input chain is added.
func TestRenderRulesetLeavesLoopbackOnlyNodesAlone(t *testing.T) {
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 1000000}}, "", nil)
	assert.NotContains(t, out, "input")
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10000")
}
