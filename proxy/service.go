// Package proxyd is the hostit-proxy engine: the dumb data plane. It dials
// control, keeps a locally persisted routing table (host -> target), and
// forwards traffic straight to app targets -- so apps keep serving while
// control or a node daemon restarts. Anything it does not recognize falls
// through to control, which owns the "nothing here" page and on-demand cert
// issuance. See plans/260807-hostit-multinode.md.
package proxy

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// internalRoutesPath/internalCertPath are control's internal-surface
	// endpoints this proxy polls (package control registers the same paths).
	internalRoutesPath = "/internal/routes"
	internalCertPath   = "/internal/cert"
	// pollTimeout is the long-poll window: control answers immediately when the
	// table changed since the caller's seq, else when this elapses.
	pollTimeout = 25 * time.Second
	// retryDelay paces redials while control is down
	retryDelay = 2 * time.Second
	// routesFile persists the last-known table, so a proxy restart during a
	// control outage still comes back serving.
	routesFile = "routes.json"
)

// Route maps one hostname to its forwarding target. Control targets (the
// dashboard/API hostnames) are not listed: unknown hosts fall through to
// control anyway.
type Route struct {
	Host   string `json:"host"`
	Target string `json:"target"` // host:port the app listens on
}

// Table is the full routing table at one point in time; Seq strictly
// increases with every change, which is what the long-poll compares.
type Table struct {
	Seq    int64   `json:"seq"`
	Routes []Route `json:"routes"`
}

// Config wires a Proxy: where control is, and where to persist the cache.
type Config struct {
	ControlURL  string // control's local HTTP listener: dashboard/API + unknown-host fallback
	InternalURL string // control's internal listener (routes + certs); defaults to ControlURL
	CacheDir    string
}

// Proxy serves from its cached table and keeps it fresh via long-polls.
type Proxy struct {
	conf    *Config
	table   atomic.Value // *Table
	control *httputil.ReverseProxy
	client  *http.Client
	certs   map[string]*tls.Certificate
	certMu  sync.Mutex // Protects certs
}

// New builds a Proxy; call WatchRoutes to start the table subscription.
func New(conf *Config) *Proxy {
	controlURL, err := url.Parse(conf.ControlURL)
	if err != nil {
		controlURL = &url.URL{Scheme: "http", Host: "127.0.0.1"}
	}
	if conf.InternalURL == "" {
		conf.InternalURL = conf.ControlURL
	}
	p := &Proxy{conf: conf, client: &http.Client{Timeout: pollTimeout + 10*time.Second}, certs: map[string]*tls.Certificate{}}
	p.table.Store(&Table{})
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
	return p.table.Load().(*Table).Seq
}

// ServeHTTP forwards a request: a known app host goes straight to its target
// (Host preserved, forwarded-for set); everything else -- the dashboard, the
// API, unknown names -- falls through to control.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	for _, route := range p.table.Load().(*Table).Routes {
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

// WatchRoutes keeps the table fresh: snapshot on start, then long-polls with
// the current seq (returns instantly on change), persisting every update.
// While control is down it keeps serving the cached table and retries.
func (p *Proxy) WatchRoutes(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		table, err := p.fetch(p.Seq())
		if err != nil {
			select {
			case <-done:
				return
			case <-time.After(retryDelay):
			}
			continue
		}
		if table.Seq != p.Seq() {
			p.table.Store(table)
			p.persist(table)
			slog.Info("Routing table updated", "seq", table.Seq, "routes", len(table.Routes))
		}
	}
}

func (p *Proxy) fetch(since int64) (*Table, error) {
	resp, err := p.client.Get(fmt.Sprintf("%s%s?since=%d", p.conf.InternalURL, internalRoutesPath, since))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var table Table
	if err := json.NewDecoder(resp.Body).Decode(&table); err != nil {
		return nil, err
	}
	return &table, nil
}

func (p *Proxy) persist(table *Table) {
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
	var table Table
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
