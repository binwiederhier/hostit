package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"heckel.io/hostit/proxyapi"
)

// Proxy metrics, exposed on the optional listen-metrics endpoint.
var (
	proxyRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hostit_proxy_requests_total",
		Help: "Requests served by the data plane, by status class and whether the host matched a route.",
	}, []string{"status", "routed"})
	proxyDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hostit_proxy_request_duration_seconds",
		Help:    "Data-plane request duration.",
		Buckets: prometheus.DefBuckets,
	})
)

// registerRoutesGauge exposes how many routes this proxy is serving, read from
// the live table on scrape.
func (p *Proxy) registerRoutesGauge() {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hostit_proxy_routes", Help: "App routes this proxy is currently serving.",
	}, func() float64 {
		if t, ok := p.table.Load().(*proxyapi.Table); ok && t != nil {
			return float64(len(t.Routes))
		}
		return 0
	})
}

// instrument records a served request's status, latency and whether its host
// matched a route (a miss falls through to control).
func instrument(status int, routed bool, start time.Time) {
	r := "no"
	if routed {
		r = "yes"
	}
	proxyRequests.WithLabelValues(strconv.Itoa(status/100)+"xx", r).Inc()
	proxyDuration.Observe(time.Since(start).Seconds())
}

// statusRecorder captures the response status for metrics.
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
