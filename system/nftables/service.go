// Package firewall applies hostit's per-app loopback restrictions with nftables:
// each app's published port is reachable on loopback only by root (the proxy, which
// owns the published container ports) and the app's own uid, so one app cannot
// reach another's port over 127.0.0.1.
package nftables

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Rule restricts loopback connects to an app port to root and the owning UID.
type Rule struct {
	Port int
	UID  int
}

// internalDropCIDRs are the destinations an app container must not reach: the
// cloud metadata endpoint (169.254.169.254), all RFC1918 private space, and
// CGNAT -- so an app cannot read droplet metadata or dial another app's
// published port / the host / the VPC. Public egress is unaffected. Matches the
// preview screenshot container's egress set. NOTE: a self-hosted install whose
// DNS resolver sits on a private IP must allow it explicitly (DO/loopback
// 127.0.0.53 resolvers are unaffected).
const internalDropCIDRs = "169.254.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10"

// internalDropCIDRs6 is the IPv6 twin: unique-local (fc00::/7, which covers AWS
// IMDS-over-IPv6 at fd00:ec2::254) and link-local (fe80::/10). Inert where a node
// has no IPv6, so it costs nothing there and protects IPv6-enabled installs.
const internalDropCIDRs6 = "fc00::/7, fe80::/10"

// appUIDs is the deduplicated, comma-joined set of app uids in the rules, for an
// nftables uid set. Empty when there are no apps.
func appUIDs(rules []Rule) string {
	seen := map[int]bool{}
	var uids []string
	for _, r := range rules {
		if !seen[r.UID] {
			seen[r.UID] = true
			uids = append(uids, strconv.Itoa(r.UID))
		}
	}
	return strings.Join(uids, ", ")
}

// splitCIDRFamilies partitions an allow-list into its IPv4 and IPv6 entries so
// each can go in the matching nftables set (ip daddr vs ip6 daddr). A ":" marks
// an IPv6 literal; everything else is treated as IPv4.
func splitCIDRFamilies(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		if strings.Contains(c, ":") {
			v6 = append(v6, c)
		} else {
			v4 = append(v4, c)
		}
	}
	return v4, v6
}

// Interface is the subset of firewall operations the machine half depends on; the
// concrete *Service satisfies it, so a test can substitute a fake.
type Interface interface {
	Apply(rules []Rule) error
}

// Service applies port rules via the nft command. Each node owns its own
// nftables table (named for its node id): two colocated nodes replacing one
// shared table would wipe each other's rules on every reconcile.
type Service struct {
	table string
	// bindAddr is where this node publishes app ports; empty means loopback
	// (a colocated node). allowFrom are the addresses permitted to reach a
	// published port -- the control plane's, since the proxy dials the apps.
	bindAddr  string
	allowFrom []string
	// localProxyUID is the uid a colocated non-root hostit-proxy runs as. The
	// per-app loopback rule allows root and the app's own uid; the proxy dials
	// those ports too, so its uid is allowed alongside. 0 means the proxy is
	// root (the default), which the { 0, ... } set already covers.
	localProxyUID int
	// allowCIDRs are internal destinations an app MAY reach despite the egress
	// drop -- the operator's outbound allow-list (outbound-allow-private-cidrs), e.g. a
	// corporate internal service or a private DNS resolver.
	allowCIDRs []string
}

var _ Interface = (*Service)(nil)

// New builds a firewall Service owning the named table ("hostit" for the
// default local node), publishing on bindAddr (empty for loopback) and
// admitting connections to published ports only from allowFrom. localProxyUID
// is the uid a colocated unprivileged proxy runs as (0 = the proxy is root).
func New(table string, bindAddr string, allowFrom []string, localProxyUID int, allowCIDRs []string) *Service {
	return &Service{table: table, bindAddr: bindAddr, allowFrom: allowFrom, localProxyUID: localProxyUID, allowCIDRs: allowCIDRs}
}

// Apply atomically replaces this node's nftables table: for each app port,
// loopback connects are only allowed for root and the app's own uid.
func (s *Service) Apply(rules []Rule) error {
	ruleset := renderRuleset(s.table, rules, s.bindAddr, s.allowFrom, s.localProxyUID, s.allowCIDRs)
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
func renderRuleset(table string, rules []Rule, bindAddr string, allowFrom []string, localProxyUID int, allowCIDRs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", table)
	fmt.Fprintf(&b, "flush table inet %s\n", table)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority filter ; policy accept ; }\n", table)
	for _, rule := range rules {
		// Allowed to reach the app port over loopback: root, the app's own uid,
		// and (when the colocated proxy is unprivileged) the proxy's uid.
		allowed := fmt.Sprintf("{ 0, %d }", rule.UID)
		if localProxyUID != 0 {
			allowed = fmt.Sprintf("{ 0, %d, %d }", rule.UID, localProxyUID)
		}
		fmt.Fprintf(&b, "add rule inet %s output ip daddr 127.0.0.0/8 tcp dport %d meta skuid != %s counter drop\n", table, rule.Port, allowed)
		fmt.Fprintf(&b, "add rule inet %s output ip6 daddr ::1 tcp dport %d meta skuid != %s counter drop\n", table, rule.Port, allowed)
	}
	// App containers must not reach the metadata endpoint or internal networks;
	// their slirp4netns egress is sourced by the app's own uid, so drop that uid's
	// traffic to the internal ranges (public egress is unaffected). Applies on a
	// single-box install too, where the loopback rules above do not cover this.
	if uids := appUIDs(rules); uids != "" {
		// The operator's outbound allow-list punches holes for whitelisted internal
		// destinations (corporate services, a private resolver). Whitelist NARROWLY:
		// whitelisting a whole node network re-exposes apps to each other. Accept
		// comes before drop, and each family goes in its own set.
		allow4, allow6 := splitCIDRFamilies(allowCIDRs)
		if len(allow4) > 0 {
			fmt.Fprintf(&b, "add rule inet %s output meta skuid { %s } ip daddr { %s } counter accept\n", table, uids, strings.Join(allow4, ", "))
		}
		if len(allow6) > 0 {
			fmt.Fprintf(&b, "add rule inet %s output meta skuid { %s } ip6 daddr { %s } counter accept\n", table, uids, strings.Join(allow6, ", "))
		}
		fmt.Fprintf(&b, "add rule inet %s output meta skuid { %s } ip daddr { %s } counter drop\n", table, uids, internalDropCIDRs)
		fmt.Fprintf(&b, "add rule inet %s output meta skuid { %s } ip6 daddr { %s } counter drop\n", table, uids, internalDropCIDRs6)
	}

	if bindAddr == "" {
		return b.String() // loopback only: the output rules are the whole story
	}
	// An app published on a real address is reachable by anything that can
	// route to this node, its public interface included. Name who may: the
	// control plane's addresses, and nothing else.
	fmt.Fprintf(&b, "add chain inet %s input { type filter hook input priority filter ; policy accept ; }\n", table)
	for _, rule := range rules {
		for _, addr := range allowFrom {
			fmt.Fprintf(&b, "add rule inet %s input ip saddr %s tcp dport %d counter accept\n", table, addr, rule.Port)
		}
		fmt.Fprintf(&b, "add rule inet %s input ip daddr %s tcp dport %d counter drop\n", table, bindAddr, rule.Port)
	}
	return b.String()
}
