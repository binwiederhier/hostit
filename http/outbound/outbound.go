// Package outbound is the HTTP client hostit uses for URLs a USER supplied.
//
// Every such fetch is a request hostit makes from inside its own network on a
// stranger's say-so. Without a guard, "add an MCP server" and "add your own
// OAuth provider" are both a server-side request forgery primitive: point one
// at the cloud metadata service and hostit reads it, from an address the
// metadata service trusts and the user could never reach themselves.
//
// The guard runs at DIAL time, on the address actually being connected to,
// rather than on the hostname. Checking the name up front does not work: a name
// that resolves public when checked and private when connected -- DNS
// rebinding -- walks straight past it. Only the dialer sees the truth.
package outbound

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const defaultTimeout = 20 * time.Second

var (
	// ErrBlockedAddress means the URL resolved somewhere hostit will not go.
	ErrBlockedAddress = errors.New("that address is not reachable from here")
	// ErrBadScheme means it was not http or https at all.
	ErrBadScheme = errors.New("only http:// and https:// URLs can be fetched")
)

// NewClient returns an HTTP client that refuses to connect to any address that
// is not publicly routable. Pass 0 for the default timeout.
//
// allowPrivate turns the guard OFF. That exists for one legitimate case: a
// self-hoster whose MCP server really is on their own LAN, or a Home Assistant
// at 192.168.1.50. It is off by default because the same setting is what stands
// between an ordinary user and the cloud metadata service, and an operator who
// turns it on should have decided to.
func NewClient(timeout time.Duration, allowPrivate bool) *http.Client {
	if timeout == 0 {
		timeout = defaultTimeout
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs after the address is resolved and before the socket is
		// connected, which is the only place the check is honest.
		Control: func(network, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			return checkDialAddress(address)
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			// A redirect is another chance to be sent somewhere internal, but
			// every hop dials through the same guarded dialer, so it is covered.
			MaxIdleConns:    10,
			IdleConnTimeout: 30 * time.Second,
		},
	}
}

func checkDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The dialer hands us a resolved address; anything else is not
		// something to guess about.
		return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
	}
	if !publiclyRoutable(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	return nil
}

// publiclyRoutable reports whether an address is one hostit will connect to.
// Everything private, local or special-purpose is refused -- including the
// link-local range, which is where every cloud provider puts its unauthenticated
// metadata service.
func publiclyRoutable(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// IPv6 forms that embed an IPv4 destination under a different representation
	// must be judged on the embedded v4, or a private/link-local target sails
	// past the checks above: 6to4 (2002::/16, bytes 2-6) and NAT64 (64:ff9b::/96,
	// bytes 12-16). IPv4-mapped/compatible are already handled by To4() below.
	if ip16 := ip.To16(); ip16 != nil && ip.To4() == nil {
		var embedded net.IP
		switch {
		case ip16[0] == 0x20 && ip16[1] == 0x02:
			embedded = net.IP(ip16[2:6])
		case ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b:
			embedded = net.IP(ip16[12:16])
		}
		if embedded != nil && !publiclyRoutable(embedded) {
			return false
		}
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) would otherwise sail past the checks
	// above under a different representation.
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10, carrier-grade NAT, is where a lot of internal
		// infrastructure hides on cloud providers.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 0.0.0.0/8 and 240.0.0.0/4 are not routable destinations either.
		if v4[0] == 0 || v4[0] >= 240 {
			return false
		}
	}
	return true
}

// CheckURL rejects a URL that cannot be fetched at all -- the wrong scheme, or
// no host. It is the cheap up-front check that gives the user a clear error at
// the moment they paste something; the DIAL guard is what actually enforces
// where a request may go.
func CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadScheme, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", ErrBadScheme, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %q has no host", ErrBadScheme, raw)
	}
	return nil
}
