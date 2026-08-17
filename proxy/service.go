// Package proxy is the hostit-proxy engine: the dumb data plane. It holds one
// connection to control, is told what to serve over it, and forwards traffic
// straight to app targets -- so apps keep serving while control or a node
// daemon restarts. Anything it does not recognize falls through to control,
// which owns the "nothing here" page and on-demand cert issuance.
//
// It is a cluster member like a node: same certificate authority, same
// transport, same direction of authority (control states, the member applies).
// What it is NOT is a node -- it holds no registry, provisions nothing, and
// keeps no state beyond a cache of control's last word.
package proxy

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/proxyapi"
	"heckel.io/hostit/proxylink"
)

var (
	// errNotLinked means the proxy is not currently connected to control, so a
	// lookup has to be answered from cache (or not at all).
	errNotLinked = errors.New("not connected to control")
	// Version is the proxy's build, reported in its heartbeat; set by the
	// binary at startup.
	Version = "dev"
)

const (
	// retryDelay paces redials while control is down
	retryDelay = 2 * time.Second
	// routesFile persists the last-known table, so a proxy restart during a
	// control outage still comes back serving.
	routesFile = "routes.json"
)

// Config wires a Proxy: who it is, where control is, and where to cache.
type Config struct {
	ProxyID string // this proxy's cluster identity (the CN of its certificate)
	// ControlURL is control's local HTTP listener: the dashboard/API upstream
	// and the unknown-host fallback. ClusterURL is control's member listener,
	// where this proxy dials in for its configuration.
	ControlURL string
	ClusterURL string
	// The cluster credentials: this proxy's certificate and the CA both sides
	// trust, minted by `hostit-control proxy add`.
	CertFile   string
	KeyFile    string
	CACertFile string
	CacheDir   string
}

// Proxy serves from the table control last pushed at it, cached to disk so a
// restart (or a control outage) still comes back serving.
type Proxy struct {
	conf    *Config
	table   atomic.Value // *proxyapi.Table
	control *httputil.ReverseProxy
	certs   map[string]*tls.Certificate
	// sink is the link back to control, for certificate lookups; a stand-in
	// that fails fast until the connection is up.
	sink atomic.Value // proxyapi.ControlSink
	// fallbacks holds self-signed stand-ins minted when nothing is cached and
	// control is unreachable, so :443 still completes a handshake.
	fallbacks map[string]*tls.Certificate
	certMu    sync.Mutex // Protects certs
}

// New builds a Proxy; call WatchRoutes to start the table subscription.
func New(conf *Config) *Proxy {
	controlURL, err := url.Parse(conf.ControlURL)
	if err != nil {
		controlURL = &url.URL{Scheme: "http", Host: "127.0.0.1"}
	}
	p := &Proxy{conf: conf, certs: map[string]*tls.Certificate{}}
	p.table.Store(&proxyapi.Table{})
	p.control = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = controlURL.Scheme
			r.Out.URL.Host = controlURL.Host
			r.Out.Host = r.In.Host // control routes by the ORIGINAL host
			r.Out.Header.Set("X-Forwarded-Proto", "https")
			r.SetXForwarded()
			r.Out.Header.Set("X-Forwarded-Proto", "https")
		},
	}
	p.loadPersisted()
	return p
}

// Seq is the cached table's sequence; zero until the first load.
func (p *Proxy) Seq() int64 {
	return p.table.Load().(*proxyapi.Table).Seq
}

// ApplyRoutes takes what control says this proxy should be serving. It
// implements proxyapi.ProxyAgent: control pushes on connect, on change, and on
// its reconcile timer.
//
// An older table is discarded rather than applied: pushes can overlap, and
// applying a stale one would un-route apps that already exist.
func (p *Proxy) ApplyRoutes(table *proxyapi.Table) error {
	if table == nil {
		return nil
	}
	if table.Seq < p.Seq() {
		slog.Warn("Ignoring an older routing table", "seq", table.Seq, "serving", p.Seq())
		return nil
	}
	if table.Seq == p.Seq() {
		return nil
	}
	p.table.Store(table)
	p.persist(table)
	slog.Info("Routing table updated", "seq", table.Seq, "routes", len(table.Routes))
	return nil
}

// Heartbeat reports what this proxy is: its build, and how much it is serving.
func (p *Proxy) Heartbeat() *proxyapi.Heartbeat {
	return &proxyapi.Heartbeat{Version: Version, Routes: len(p.table.Load().(*proxyapi.Table).Routes)}
}

// ServeHTTP forwards a request: a known app host goes straight to its target
// (Host preserved, forwarded-for set); everything else -- the dashboard, the
// API, unknown names -- falls through to control.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	for _, route := range p.table.Load().(*proxyapi.Table).Routes {
		if route.Host == host {
			target := route.Target
			rp := &httputil.ReverseProxy{
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.Out.URL.Scheme = "http"
					pr.Out.URL.Host = target
					pr.Out.Host = pr.In.Host
					pr.SetXForwarded()
					pr.Out.Header.Set("X-Forwarded-Proto", "https")
				},
				ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
					// Same posture as control's proxy: a dead target is a 404, not a 502
					slog.Warn("Proxy target error", "host", host, "target", target, "error", err)
					p.control.ServeHTTP(w, r)
				},
			}
			rp.ServeHTTP(w, r)
			return
		}
	}
	p.control.ServeHTTP(w, r)
}

// Link keeps one connection to control up for the proxy's whole life: it
// dials, serves control's pushes over it, and redials when it drops. Nothing
// is fetched on a schedule -- control states the table, and the connection is
// also what carries certificate lookups the other way.
//
// While control is unreachable the proxy keeps serving its cached table, which
// is the whole reason the cache exists.
func (p *Proxy) Link(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		if err := p.connect(); err != nil {
			slog.Warn("Cannot reach control; serving the cached routing table", "error", err)
		}
		p.sink.Store(noSink{})
		select {
		case <-done:
			return
		case <-time.After(retryDelay):
		}
	}
}

// connect dials control and blocks until that connection dies.
func (p *Proxy) connect() error {
	tlsConf, err := cluster.DialCreds(p.conf.CertFile, p.conf.KeyFile, p.conf.CACertFile)
	if err != nil {
		return err
	}
	conn, err := tls.Dial("tcp", p.conf.ClusterURL, tlsConf)
	if err != nil {
		return err
	}
	defer conn.Close()
	slog.Info("Connected to control", "addr", p.conf.ClusterURL, "proxy", p.conf.ProxyID)
	return proxylink.ServeAgent(conn, p.conf.ProxyID, p, func(client *http.Client) {
		p.sink.Store(proxylink.NewControlSink(client))
	})
}

// controlSink is the link to control, or a stand-in that fails fast while the
// connection is down (so a handshake falls through to the cache immediately
// rather than waiting on a dial).
func (p *Proxy) controlSink() proxyapi.ControlSink {
	if sink, ok := p.sink.Load().(proxyapi.ControlSink); ok && sink != nil {
		return sink
	}
	return noSink{}
}

// noSink stands in while control is unreachable.
type noSink struct{}

func (noSink) CertFor(string) (*proxyapi.CertMaterial, error) {
	return nil, errNotLinked
}

func (p *Proxy) persist(table *proxyapi.Table) {
	b, err := json.Marshal(table)
	if err != nil {
		return
	}
	if err := os.MkdirAll(p.conf.CacheDir, 0o700); err != nil {
		return
	}
	tmp := filepath.Join(p.conf.CacheDir, routesFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(p.conf.CacheDir, routesFile))
}

func (p *Proxy) loadPersisted() {
	b, err := os.ReadFile(filepath.Join(p.conf.CacheDir, routesFile))
	if err != nil {
		return
	}
	var table proxyapi.Table
	if err := json.Unmarshal(b, &table); err != nil {
		return
	}
	p.table.Store(&table)
}

// hostOnly strips a port from a Host header value.
func hostOnly(host string) string {
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
