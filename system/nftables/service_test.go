package nftables

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderRuleset(t *testing.T) {
	t.Parallel()
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 200000}, {Port: 10001, UID: 265536}}, "", nil, 0, nil)

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
	out := renderRuleset("hostit", nil, "", nil, 0, nil)
	assert.Contains(t, out, "flush table inet hostit")
	assert.NotContains(t, out, "drop")
	// One line each for table/flush/chain.
	assert.Equal(t, 3, strings.Count(out, "\n"))
}

func TestRulesetUsesTheNodeTable(t *testing.T) {
	t.Parallel()
	// Two colocated nodes must own separate tables, or each reconcile wipes
	// the other node's rules.
	out := renderRuleset("hostit_stage_node_2", []Rule{{Port: 10000, UID: 200000}}, "", nil, 0, nil)
	assert.Contains(t, out, "add table inet hostit_stage_node_2")
	assert.NotContains(t, out, "inet hostit ")
}

// An app published off loopback is reachable over the network, so the ruleset
// must say who may reach it: the proxies, nobody else. (Nothing in the control
// plane dials an app port -- a proxy does.)
// Without this, publishing on a node's VPC address would expose every app's
// port to anything that can route to the node -- including its public
// interface.
func TestRenderRulesetGuardsPortsPublishedOffLoopback(t *testing.T) {
	rules := []Rule{{Port: 10000, UID: 1000000}}
	out := renderRuleset("hostit_worker", rules, "10.0.0.2", []string{"10.0.0.1"}, 0, nil)

	assert.Contains(t, out, "add chain inet hostit_worker input", "an input chain exists once ports leave loopback")
	assert.Contains(t, out, "ip saddr 10.0.0.1 tcp dport 10000 counter accept", "an allowed address may reach the app")
	assert.Contains(t, out, "ip daddr 10.0.0.2 tcp dport 10000 counter drop", "everyone else is dropped")
	// The loopback isolation between apps on the same node is unchanged.
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10000 meta skuid != { 0, 1000000 } counter drop")
}

// A colocated node publishes on loopback, so there is nothing to guard and no
// input chain is added.
func TestRenderRulesetLeavesLoopbackOnlyNodesAlone(t *testing.T) {
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 1000000}}, "", nil, 0, nil)
	assert.NotContains(t, out, "input")
	assert.Contains(t, out, "ip daddr 127.0.0.0/8 tcp dport 10000")
}

// A non-root colocated proxy reaches app ports over loopback too, so its uid
// must be allowed alongside root and the app's own -- otherwise the per-app
// skuid drop (which exists so one app cannot reach another's port) also blocks
// the proxy, and every locally-hosted app goes unreachable.
func TestRenderRulesetAllowsTheLocalProxyUID(t *testing.T) {
	out := renderRuleset("hostit", []Rule{{Port: 10001, UID: 1065536}}, "", nil, 1002, nil)
	if !strings.Contains(out, "meta skuid != { 0, 1065536, 1002 } counter drop") {
		t.Errorf("the loopback rule must allow the local proxy uid (1002) too, got:\n%s", out)
	}
}

// App containers must not reach the cloud metadata endpoint or internal networks
// (another app's published port, the host, the VPC): drop the app uids' egress to
// the internal ranges. Their slirp4netns egress is sourced by the app's own uid.
func TestRenderRulesetBlocksAppEgressToInternal(t *testing.T) {
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 200000}, {Port: 10001, UID: 265536}}, "", nil, 0, nil)
	// The drop rule lists the app uids and the internal CIDRs, including metadata.
	if !strings.Contains(out, "meta skuid { 200000, 265536 } ip daddr { 169.254.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 } counter drop") {
		t.Errorf("app egress to the internal ranges must be dropped, got:\n%s", out)
	}
	// It applies even on a loopback-only (single-box) node.
	if !strings.Contains(out, "169.254.0.0/16") {
		t.Errorf("metadata endpoint must be blocked on a single-box node too")
	}
}

// With no apps there is no uid set, so no egress-drop rule is emitted.
func TestRenderRulesetNoEgressDropWithoutApps(t *testing.T) {
	out := renderRuleset("hostit", nil, "", nil, 0, nil)
	if strings.Contains(out, "169.254.0.0/16") {
		t.Errorf("no app uids -> no egress drop rule, got:\n%s", out)
	}
}

// The operator's outbound allow-list punches holes in the internal egress drop
// (corporate internal services, a private resolver) without opening public
// filtering. Whitelisted CIDRs are accepted BEFORE the internal drop.
func TestRenderRulesetHonorsTheEgressAllowList(t *testing.T) {
	out := renderRuleset("hostit", []Rule{{Port: 10000, UID: 200000}}, "", nil, 0, []string{"10.20.0.0/24", "10.30.5.10/32"})
	accept := "meta skuid { 200000 } ip daddr { 10.20.0.0/24, 10.30.5.10/32 } counter accept"
	drop := "meta skuid { 200000 } ip daddr { 169.254.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 } counter drop"
	ai := strings.Index(out, accept)
	di := strings.Index(out, drop)
	if ai < 0 || di < 0 {
		t.Fatalf("expected both the allow-list accept and the internal drop, got:\n%s", out)
	}
	if ai > di {
		t.Errorf("the allow-list accept must come BEFORE the internal drop (else it is dead), got accept@%d drop@%d", ai, di)
	}
}
