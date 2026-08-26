package proxy

import (
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
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
