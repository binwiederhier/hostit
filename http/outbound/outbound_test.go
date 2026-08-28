package outbound

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	nets, err := ParseCIDRs(cidrs)
	require.NoError(t, err)
	return nets
}

// Anything that fetches a URL a USER supplied is a request hostit makes from
// inside its own network on a stranger's say-so. Without a guard that reaches
// the cloud metadata service, every internal admin panel, and every service
// bound to loopback on the control host.
func TestALoopbackAddressIsRefused(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the guarded client connected to an internal service")
	}))
	t.Cleanup(internal.Close)

	_, err := NewClient(0, nil).Get(internal.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
}

func TestThePrivateRangesAreRefused(t *testing.T) {
	for _, host := range []string{
		"http://127.0.0.1/x",
		"http://localhost/x",
		"http://10.0.0.1/x",
		"http://192.168.1.1/x",
		"http://172.16.0.1/x",
		// The one that matters most on a cloud box: the metadata service,
		// unauthenticated on most providers and full of credentials.
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/x",
		"http://[fd00::1]/x",
	} {
		_, err := NewClient(0, nil).Get(host)
		require.Error(t, err, host)
		assert.ErrorIs(t, err, ErrBlockedAddress, host)
	}
}

// A public address still works, or the guard would have broken the feature it
// is protecting.
// The escape hatch: a self-hoster whose MCP server really is on their own LAN.
func TestPrivateAddressesAreAllowedWhenTheOperatorSaysSo(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(internal.Close)

	resp, err := NewClient(0, mustCIDRs(t, "127.0.0.0/8", "::1/128")).Get(internal.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPublicAddressIsAllowed(t *testing.T) {
	assert.NoError(t, CheckURL("https://mcp.example.com/mcp"))
	assert.NoError(t, CheckURL("https://93.184.216.34/x"))
}

// The allow-list opens only the ranges it names. Naming a LAN range must NOT
// also open every other private address, and above all not the metadata service
// -- which is the whole point of a list instead of an all-or-nothing switch.
func TestAnAllowedCIDROpensOnlyThatRange(t *testing.T) {
	allow := mustCIDRs(t, "192.168.0.0/16")
	assert.NoError(t, checkDialAddress("192.168.1.50:8080", allow), "an address in the range is allowed")
	assert.ErrorIs(t, checkDialAddress("10.0.0.1:80", allow), ErrBlockedAddress, "a private address outside the range is still refused")
	assert.ErrorIs(t, checkDialAddress("169.254.169.254:80", allow), ErrBlockedAddress, "the metadata service stays blocked unless explicitly listed")
	assert.NoError(t, checkDialAddress("93.184.216.34:443", allow), "a public address is always allowed")
	// An empty list is the strict default: nothing private gets through.
	assert.ErrorIs(t, checkDialAddress("192.168.1.50:80", nil), ErrBlockedAddress)
}

// A single host can be allowed with a /32 (or /128), including the metadata
// service if the operator really means to.
func TestAnAllowedCIDRCanBeASingleHost(t *testing.T) {
	allow := mustCIDRs(t, "169.254.169.254/32")
	assert.NoError(t, checkDialAddress("169.254.169.254:80", allow))
	assert.ErrorIs(t, checkDialAddress("169.254.169.253:80", allow), ErrBlockedAddress)
}

func TestParseCIDRsNamesABadEntry(t *testing.T) {
	_, err := ParseCIDRs([]string{"192.168.0.0/16", "not-a-cidr"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-cidr")

	nets, err := ParseCIDRs([]string{" 10.0.0.0/8 ", "", "fd00::/8"})
	require.NoError(t, err)
	assert.Len(t, nets, 2, "blank entries are skipped, valid ones kept")
}

// The scheme matters too: file:// and gopher:// are not things to hand a
// user-supplied URL to.
func TestOnlyHTTPSchemesAreAllowed(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "gopher://x/", "ftp://x/", "x"} {
		assert.Error(t, CheckURL(raw), raw)
	}
}

// The guard runs at DIAL time, on the address actually being connected to.
// Checking the hostname up front is not enough: a name that resolves public
// when the connection is made and private a second later (DNS rebinding) would
// walk straight past a check done any earlier.
func TestTheGuardRunsOnTheResolvedAddressNotTheName(t *testing.T) {
	// "localhost" passes a naive string check for a private IP and still
	// resolves to loopback.
	_, err := NewClient(0, nil).Do(mustRequest(t, "http://localhost:9/x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	return req
}
