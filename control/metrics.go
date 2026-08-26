package control

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Control-plane metrics, registered on the default registry at package load and
// exposed on the optional listen-metrics endpoint (see the metrics package).
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hostit_control_http_requests_total",
		Help: "Control-plane HTTP requests by method, matched route pattern and status.",
	}, []string{"method", "route", "status"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hostit_control_http_request_duration_seconds",
		Help:    "Control-plane HTTP request duration by method and route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	deploysTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hostit_control_deploys_total",
		Help: "App deploys requested through the control plane.",
	})
)

// instrumentHTTP records request count and latency labelled by the matched
// ServeMux route pattern (bounded cardinality), not the raw path.
func instrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// r.Pattern is the matched ServeMux pattern (e.g. "GET /apps/{name}"),
		// method-prefixed; keep only the path part since method is its own label.
		route := r.Pattern
		if i := strings.IndexByte(route, ' '); i >= 0 {
			route = route[i+1:]
		}
		if route == "" {
			route = "other"
		}
		httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// A wrapped ResponseWriter must still expose Hijacker/Flusher, or WebSocket
// upgrades (the terminal, app WebSockets) and SSE break.
var (
	_ http.Hijacker = (*statusRecorder)(nil)
	_ http.Flusher  = (*statusRecorder)(nil)
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack forwards to the underlying writer so WebSocket upgrades (the terminal,
// app WebSockets) keep working through this wrapper.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter is not a http.Hijacker")
}

// Flush forwards to the underlying writer so streaming responses (SSE) keep
// flushing through this wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// registerMetrics wires the on-scrape fleet gauges. Called once from Run when
// metrics are enabled, so tests (which never enable them) do not double-register.
func (s *Server) registerMetrics() {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hostit_control_apps", Help: "Apps in the registry.",
	}, func() float64 {
		apps, err := s.apps.Store().Apps()
		if err != nil {
			return 0
		}
		return float64(len(apps))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hostit_control_users", Help: "Registered users.",
	}, func() float64 {
		users, err := s.users.Users()
		if err != nil {
			return 0
		}
		return float64(len(users))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hostit_control_nodes_connected", Help: "Node agents currently dialed in.",
	}, func() float64 {
		return float64(len(s.apps.NodeRegistry().IDs()))
	})
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hostit_control_ssh_relay_enabled", Help: "1 if the optional SSH relay gateway is on.",
	}, func() float64 {
		if s.config.SSHRelayEnabled {
			return 1
		}
		return 0
	})
}
