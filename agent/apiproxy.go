package agent

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"

	"heckel.io/hostit/control/config"
)

// apiProxyHandler forwards every request to the container's unix socket, so the
// whole container API is reachable over a plain loopback TCP address in addition
// to the socket (see Agent.serveContainerAPI). An app can then use an ordinary
// HTTP client and URL instead of dialing a unix socket, which most languages
// make awkward.
//
// It preserves the socket's identity for free: the agent connects as the
// container's root, which the idmap maps to the app's host uid -- the same uid
// the app's own process would present -- so the node's SO_PEERCRED check still
// sees the right app.
func apiProxyHandler(socketPath string) http.Handler {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// The host is meaningless over a unix socket, but net/http requires
			// a scheme and host to route the request; the dialer ignores both.
			req.URL.Scheme = "http"
			req.URL.Host = "hostit.sock"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// serveContainerAPI runs the loopback listener for the container's lifetime. A
// bind failure is logged, not fatal: the app must still come up (and can still
// use the unix socket) if, say, something already holds the address.
func (a *Agent) serveContainerAPI() {
	ln, err := net.Listen("tcp", config.ContainerAPIAddr)
	if err != nil {
		slog.Warn("Container API not served on TCP; the unix socket is unaffected",
			"addr", config.ContainerAPIAddr, "error", err)
		return
	}
	slog.Info("Container API on loopback, in addition to the unix socket", "addr", config.ContainerAPIAddr)
	if err := http.Serve(ln, apiProxyHandler(config.DefaultSocketFile)); err != nil {
		slog.Warn("Container API loopback listener stopped", "error", err)
	}
}
