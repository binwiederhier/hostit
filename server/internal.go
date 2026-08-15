package server

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The internal surface: what hostit-proxy (and later hostit-node enrollment)
// consume over the internal listener -- never the public one. It carries no
// tenant auth; the transport is the boundary (a root-only unix socket when
// colocated, mTLS across hosts).

var errTLSNotManaged = errors.New("tls is not managed here")

const (
	// internalPollInterval is how often a blocked routes long-poll re-checks
	// for changes; the table is tiny, so recomputing is cheaper than threading
	// change hooks through every mutation site.
	internalPollInterval = 500 * time.Millisecond
	// internalPollMax caps a long-poll, whatever the client asked for
	internalPollMax = 25 * time.Second
)

// routeTable mirrors proxyd.Table on the wire (seq/routes/host/target).
type routeTable struct {
	Seq    int64        `json:"seq"`
	Routes []routeEntry `json:"routes"`
}

type routeEntry struct {
	Host   string `json:"host"`
	Target string `json:"target"`
}

// Internal returns the internal API handler.
func (s *Server) Internal() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/routes", s.handleInternalRoutes)
	mux.HandleFunc("GET /internal/cert", s.handleInternalCert)
	return mux
}

// handleInternalRoutes serves the routing table: immediately when the caller
// has no (or a stale) seq, else long-polling until the table changes or the
// window closes -- the proxy's push-ish subscription.
func (s *Server) handleInternalRoutes(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	wait := internalPollMax
	if secs, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil && secs > 0 && time.Duration(secs)*time.Second < internalPollMax {
		wait = time.Duration(secs) * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		table, err := s.currentRoutes()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if table.Seq != since || time.Now().After(deadline) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(table)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(internalPollInterval):
		}
	}
}

// currentRoutes builds the table from the registry and assigns it a sequence
// number that bumps exactly when the content changes (a hash comparison, so no
// mutation site needs to notify).
func (s *Server) currentRoutes() (*routeTable, error) {
	apps, err := s.apps.Store().Apps()
	if err != nil {
		return nil, err
	}
	domains, err := s.apps.Store().ActiveDomains()
	if err != nil {
		return nil, err
	}
	routes := make([]routeEntry, 0, len(apps)+len(domains))
	targets := make(map[string]string, len(apps))
	for _, a := range apps {
		// Today every app is local to control's host; the multi-node phase turns
		// Host into the node address here.
		target := fmt.Sprintf("127.0.0.1:%d", a.Port)
		targets[a.Name] = target
		routes = append(routes, routeEntry{Host: a.Name + "." + s.config.BaseDomain, Target: target})
	}
	for appName, domain := range domains {
		if target, ok := targets[appName]; ok {
			routes = append(routes, routeEntry{Host: domain, Target: target})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Host < routes[j].Host })

	// Hash the content; a changed hash bumps the persistent-ish seq (in memory:
	// proxies compare seqs within one control lifetime, and a restart's seq
	// reset just causes one redundant snapshot).
	b, err := json.Marshal(routes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(b)
	sum := hex.EncodeToString(digest[:])
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	if sum != s.routesHash {
		s.routesHash = sum
		s.routesSeq++
	}
	return &routeTable{Seq: s.routesSeq, Routes: routes}, nil
}

// certMaterial is what the proxy needs to terminate TLS for one SNI name.
type certMaterial struct {
	CertPEM string `json:"cert_pem"` // full chain
	KeyPEM  string `json:"key_pem"`
}

// handleInternalCert hands the data plane the certificate for one SNI name,
// through the exact combined lookup control's own TLS would use -- including
// on-demand issuance for not-yet-seen custom domains. Nodes never see keys;
// proxies must, since they terminate.
func (s *Server) handleInternalCert(w http.ResponseWriter, r *http.Request) {
	if s.tlsGetCert == nil {
		writeError(w, http.StatusServiceUnavailable, errTLSNotManaged)
		return
	}
	sni := r.URL.Query().Get("sni")
	cert, err := s.tlsGetCert(&tls.ClientHelloInfo{ServerName: sni})
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var chain bytes.Buffer
	for _, der := range cert.Certificate {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var key bytes.Buffer
	_ = pem.Encode(&key, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&certMaterial{CertPEM: chain.String(), KeyPEM: key.String()})
}

// listenInternal starts the internal surface on a unix socket (colocated
// default: root-only by mode) or a TCP address (private interfaces / mTLS
// fronting in later phases).
func (s *Server) listenInternal() error {
	addr := s.config.ListenInternal
	srv := &http.Server{Handler: s.Internal()}
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		_ = os.Remove(path)
		ln, err := net.Listen("unix", path)
		if err != nil {
			return fmt.Errorf("internal listener: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		s.servers = append(s.servers, srv)
		go func() {
			slog.Info("Listening for internal API", "socket", path)
			_ = srv.Serve(ln)
		}()
		return nil
	}
	srv.Addr = addr
	s.servers = append(s.servers, srv)
	go func() {
		slog.Info("Listening for internal API", "addr", addr)
		_ = srv.ListenAndServe()
	}()
	return nil
}
