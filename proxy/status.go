package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/proxy/api"
)

// Status is what `hostit proxy status` prints: this proxy's identity, whether
// its control link is up, and how much it is serving. It is the PROXY's own
// view, answered from its cache -- readable while control is down, which is
// exactly the situation the cache exists for.
type Status struct {
	ProxyID     string `json:"proxy_id"`
	Version     string `json:"version"`
	ControlURL  string `json:"control_url"`
	ClusterURL  string `json:"cluster_url"`
	Connected   bool   `json:"connected"`
	TableSeq    int64  `json:"table_seq"`
	Routes      int    `json:"routes"`
	CertsCached int    `json:"certs_cached"`
}

// Connected reports whether the link to control is currently up. The sink is
// dropped (boxed nil) between connections, which is what this reads.
func (p *Proxy) Connected() bool {
	ref := p.sink.Load()
	return ref != nil && ref.sink != nil
}

// Status assembles the proxy's own status.
func (p *Proxy) Status() *Status {
	table := p.table.Load().(*api.Table)
	p.certMu.Lock()
	certs := len(p.certs)
	p.certMu.Unlock()
	return &Status{
		ProxyID:     p.conf.ProxyID,
		Version:     Version,
		ControlURL:  p.conf.ControlURL,
		ClusterURL:  p.conf.ClusterURL,
		Connected:   p.Connected(),
		TableSeq:    table.Seq,
		Routes:      len(table.Routes),
		CertsCached: certs,
	}
}

// Routes returns the routing table the proxy is serving from right now.
func (p *Proxy) Routes() *api.Table {
	return p.table.Load().(*api.Table)
}

// ServeStatusSocket serves the proxy's status and routing table on a root-only
// unix socket, for `hostit proxy status` and `hostit proxy route list`.
// cluster.ListenSocket's 0600 is the whole story: only root may ask, and
// nothing served here mutates anything.
func ServeStatusSocket(path string, p *Proxy) (io.Closer, error) {
	listener, err := cluster.ListenSocket(path)
	if err != nil {
		return nil, err
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, p.Status())
	})
	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, p.Routes())
	})
	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("Status socket server failed", "socket", path, "error", err)
		}
	}()
	return listener, nil
}
