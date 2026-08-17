package control

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
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/store"
)

// The internal surface: what hostit-proxy (and later hostit-node enrollment)
// consume over the internal listener -- never the public one. It carries no
// tenant auth; the transport is the boundary (a root-only unix socket when
// colocated, mTLS across hosts).

var errTLSNotManaged = errors.New("tls is not managed here")

const (
	// internalRoutesPath/internalCertPath are the internal-surface endpoints the
	// data plane (hostit-proxy) polls; the same strings live in the proxy
	// package, which reaches this surface from the other side.
	internalRoutesPath = "/internal/routes"
	internalCertPath   = "/internal/cert"
	// internalCertTimeout bounds one cert lookup, including a possible on-demand
	// issuance for a not-yet-seen custom domain
	internalCertTimeout = 90 * time.Second
	// internalPollInterval is how often a blocked routes long-poll re-checks
	// for changes; the table is tiny, so recomputing is cheaper than threading
	// change hooks through every mutation site.
	internalPollInterval = 500 * time.Millisecond
	// internalPollMax caps a long-poll, whatever the client asked for
	internalPollMax = 25 * time.Second
)

// routeTable mirrors proxy.Table on the wire (seq/routes/host/target).
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
	mux.HandleFunc("GET "+internalRoutesPath, s.handleInternalRoutes)
	mux.HandleFunc("GET "+internalCertPath, s.handleInternalCert)
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
		addr := s.nodeAddress(a.Host)
		if addr == "" {
			continue // its node has not reported where to reach it yet
		}
		target := fmt.Sprintf("%s:%d", addr, a.Port)
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
	// proxies compare versions across control lifetimes, which is why the
	// counter is persisted below).
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
		// Persisted, not just in memory: the proxy keeps the last version it
		// stored across ITS restarts and long-polls with it, so a control that
		// restarted its counter would hand out a number the proxy already
		// holds -- and the proxy would block on an equal version forever while
		// serving a stale table.
		if err := s.apps.Store().SetSetting(store.SettingRoutesSeq, strconv.FormatInt(s.routesSeq, 10)); err != nil {
			slog.Warn("Cannot persist the routing table version", "error", err)
		}
	}
	return &routeTable{Seq: s.routesSeq, Routes: routes}, nil
}

// nodeAddress resolves an app's hosting node to the address its ports are
// dialed at: control's own loopback for the local node (and for anything
// unknown -- wrong-but-local beats black-holing a route), the registered
// node address otherwise.
func (s *Server) nodeAddress(host string) string {
	if host == "" || host == store.HostLocal {
		return "127.0.0.1"
	}
	if n, err := s.apps.Store().Node(host); err == nil && n.Address != "" {
		return n.Address
	}
	// No loopback fallback for a remote node: its apps are NOT here, and
	// pointing the proxy at this host would serve someone else's app (or
	// nothing) under that hostname. Omitting the route 404s honestly until the
	// node reports its address on connect.
	slog.Warn("No address for node; omitting its routes until it reports one", "node", host)
	return ""
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
	// A non-nil Conn: certmagic inspects the hello's connection addresses.
	cert, err := s.tlsGetCert(&tls.ClientHelloInfo{ServerName: sni, Conn: internalHelloConn{}})
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

// internalHelloConn is the synthetic connection behind the internal cert
// endpoint's ClientHelloInfo -- certmagic dereferences the hello's Conn for
// addresses, so it must not be nil.
type internalHelloConn struct{}

func (internalHelloConn) Read([]byte) (int, error)  { return 0, io.EOF }
func (internalHelloConn) Write([]byte) (int, error) { return 0, io.EOF }
func (internalHelloConn) Close() error              { return nil }
func (internalHelloConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
}
func (internalHelloConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}
func (internalHelloConn) SetDeadline(time.Time) error      { return nil }
func (internalHelloConn) SetReadDeadline(time.Time) error  { return nil }
func (internalHelloConn) SetWriteDeadline(time.Time) error { return nil }
