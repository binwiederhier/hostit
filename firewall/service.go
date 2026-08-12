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

// Service applies port rules via the nft command.
type Service struct{}

// New builds a firewall Service.
func New() *Service {
	return &Service{}
}

// Apply atomically replaces the hostit nftables table: for each app port, loopback
// connects are only allowed for root and the app's own uid.
func (s *Service) Apply(rules []Rule) error {
	ruleset := renderRuleset(rules)
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

// renderRuleset builds the nft ruleset that replaces the hostit table, split out so
// the generated rules can be tested without invoking nft.
func renderRuleset(rules []Rule) string {
	var b strings.Builder
	b.WriteString("add table inet hostit\n")
	b.WriteString("flush table inet hostit\n")
	b.WriteString("add chain inet hostit output { type filter hook output priority filter ; policy accept ; }\n")
	for _, rule := range rules {
		fmt.Fprintf(&b, "add rule inet hostit output ip daddr 127.0.0.0/8 tcp dport %d meta skuid != { 0, %d } counter drop\n", rule.Port, rule.UID)
		fmt.Fprintf(&b, "add rule inet hostit output ip6 daddr ::1 tcp dport %d meta skuid != { 0, %d } counter drop\n", rule.Port, rule.UID)
	}
	return b.String()
}
