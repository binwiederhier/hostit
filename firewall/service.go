// Package firewall applies hostit's per-app loopback restrictions with nftables:
// each app's published port is reachable on loopback only by root (the proxy, which
// owns the published container ports) and the app's own uid, so one app cannot
// reach another's port over 127.0.0.1.
package firewall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Rule restricts loopback connects to an app port to root and the owning UID.
type Rule struct {
	Port int
	UID  int
}

// Interface is the subset of firewall operations the app package depends on; the
// concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	Apply(rules []Rule) error
}

// Service applies port rules via the nft command. Each node owns its own
// nftables table (named for its node id): two colocated nodes replacing one
// shared table would wipe each other's rules on every reconcile.
type Service struct {
	table string
}

var _ Interface = (*Service)(nil)

// New builds a firewall Service owning the named table ("hostit" for the
// default local node).
func New(table string) *Service {
	return &Service{table: table}
}

// Apply atomically replaces this node's nftables table: for each app port,
// loopback connects are only allowed for root and the app's own uid.
func (s *Service) Apply(rules []Rule) error {
	ruleset := renderRuleset(s.table, rules)
	f, err := os.CreateTemp("", "hostit-nft-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(ruleset); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	out, err := exec.Command("nft", "-f", f.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft -f failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// renderRuleset builds the nft ruleset that replaces the node's table, split
// out so the generated rules can be tested without invoking nft.
func renderRuleset(table string, rules []Rule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", table)
	fmt.Fprintf(&b, "flush table inet %s\n", table)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority filter ; policy accept ; }\n", table)
	for _, rule := range rules {
		fmt.Fprintf(&b, "add rule inet %s output ip daddr 127.0.0.0/8 tcp dport %d meta skuid != { 0, %d } counter drop\n", table, rule.Port, rule.UID)
		fmt.Fprintf(&b, "add rule inet %s output ip6 daddr ::1 tcp dport %d meta skuid != { 0, %d } counter drop\n", table, rule.Port, rule.UID)
	}
	return b.String()
}
