// Package metrics serves a component's Prometheus metrics on an optional,
// separate listener (the `listen-metrics` config option). The metrics
// themselves are defined and updated by each component (control, node, proxy)
// against the default registry, which already carries the Go runtime and
// process collectors -- so `/metrics` exposes those with no extra wiring.
package metrics

import (
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Serve starts an HTTP server exposing Prometheus metrics at /metrics on addr
// (e.g. "10.0.0.1:9110" or ":9110") and returns it so the caller can shut it
// down. An empty addr is a no-op (returns nil, nil): metrics are opt-in, meant
// for an internal/monitoring interface, and are served without authentication.
func Serve(addr string) (*http.Server, error) {
	if addr == "" {
		return nil, nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/metrics", http.StatusFound)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
