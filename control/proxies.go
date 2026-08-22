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
	"sort"
	"sync"
	"time"

	"heckel.io/hostit/proxyapi"
	"heckel.io/hostit/store"
)

// The proxy plane: control decides what every hostname resolves to and states
// it at the proxies, the same way it states a node's desired configuration.
// Nothing here answers a poll -- a proxy holds a connection, and control
// pushes down it. Certificates go the other way, because the trigger is a
// handshake for a name the proxy has never seen.

var (
	errTLSNotManaged = errors.New("tls is not managed here")
)

const (
	// certTimeout bounds one certificate lookup for a proxy, including a
	// possible on-demand issuance for a custom domain nobody has asked for yet.
	certTimeout = 90 * time.Second
	// proxyHeartbeatInterval is how often control asks each connected proxy how
	// it is. It doubles as the liveness record an operator reads, and as the
	// interval within which a removed proxy loses its session.
	proxyHeartbeatInterval = 30 * time.Second
	// routeWatchInterval is how often control re-derives the table to notice a
	// change worth pushing. The table is small and the query cheap, so
	// recomputing beats threading a notification through every mutation site
	// (create, delete, rename, move, domain verify, a node reporting its
	// address). It bounds how long a new app waits to be routable.
	routeWatchInterval = 500 * time.Millisecond
)

// ProxyRegistry holds the proxies currently connected. A proxy is only in here
// while its session is alive, so pushing to everything in the map is the same
// as pushing to every proxy that can be reached.
type ProxyRegistry struct {
	agents map[string]proxyapi.ProxyAgent
	mu     sync.Mutex // Protects agents
}

func NewProxyRegistry() *ProxyRegistry {
	return &ProxyRegistry{agents: map[string]proxyapi.ProxyAgent{}}
}

// Register records a connected proxy, replacing whatever it had under that id
// (a reconnect supersedes the old session).
func (r *ProxyRegistry) Register(proxyID string, agent proxyapi.ProxyAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[proxyID] = agent
}

// Unregister drops a proxy, but only if the agent given is still the one
// registered: a dying session must not evict the reconnect that replaced it.
func (r *ProxyRegistry) Unregister(proxyID string, agent proxyapi.ProxyAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.agents[proxyID]; ok && current == agent {
		delete(r.agents, proxyID)
	}
}

// Agents returns a snapshot of the connected proxies, so a fan-out never holds
// the lock while it talks over the wire.
func (r *ProxyRegistry) Agents() map[string]proxyapi.ProxyAgent {
	r.mu.Lock()
	defer r.mu.Unlock()
	agents := make(map[string]proxyapi.ProxyAgent, len(r.agents))
	for id, agent := range r.agents {
		agents[id] = agent
	}
	return agents
}

// Proxies returns the connected-proxy registry.
func (s *Server) Proxies() *ProxyRegistry {
	return s.proxies
}

// PushRoutes hands every connected proxy the current table. Called when a
// proxy connects, when the table changes, and on the reconcile timer -- so a
// proxy that missed a push converges at the next one rather than staying
// stale until something else happens.
func (s *Server) PushRoutes() {
	table, err := s.Routes()
	if err != nil {
		slog.Warn("Cannot build the routing table", "error", err)
		return
	}
	for id, agent := range s.proxies.Agents() {
		if err := agent.ApplyRoutes(table); err != nil {
			slog.Warn("Cannot push the routing table to a proxy", "proxy", id, "error", err)
		}
	}
}

// RouteLoop watches for table changes and pushes them, until done closes. It
// also re-pushes on every tick where the seq moved, which is what makes a
// missed push self-correcting.
func (s *Server) RouteLoop(done <-chan struct{}) {
	slog.Info("Starting the route push loop", "interval", routeWatchInterval)
	defer slog.Info("Stopping the route push loop")
	var pushed int64
	for {
		select {
		case <-done:
			return
		case <-time.After(routeWatchInterval):
		}
		table, err := s.Routes()
		if err != nil {
			continue
		}
		if table.Seq == pushed {
			continue
		}
		pushed = table.Seq
		s.PushRoutes()
	}
}

// ProxyHeartbeatLoop keeps every connected proxy's liveness current until done
// closes.
func (s *Server) ProxyHeartbeatLoop(done <-chan struct{}) {
	slog.Info("Starting the proxy heartbeat loop", "interval", proxyHeartbeatInterval)
	defer slog.Info("Stopping the proxy heartbeat loop")
	for {
		select {
		case <-done:
			return
		case <-time.After(proxyHeartbeatInterval):
		}
		s.proxyHeartbeatPass()
	}
}

// proxyHeartbeatPass asks each connected proxy how it is and records that it
// answered, so `proxy list` reports liveness rather than the moment it
// connected. It also enforces removal: `hostit-control proxy remove` runs in a
// separate process and only deletes the registry row, so without this a
// removed proxy would keep its session (and keep being pushed routes) until it
// happened to reconnect.
func (s *Server) proxyHeartbeatPass() {
	for id, agent := range s.proxies.Agents() {
		if _, err := s.apps.Store().Proxy(id); errors.Is(err, store.ErrProxyNotFound) {
			slog.Info("Proxy was removed; dropping its session", "proxy", id)
			s.proxies.Unregister(id, agent)
			continue
		}
		// A nil answer means the proxy is connected but not responding. Leave the
		// session alone -- the transport tears it down when it actually dies, and
		// the stale last-seen is itself the signal an operator wants to see.
		hb := agent.Heartbeat()
		if hb == nil {
			slog.Warn("A proxy did not answer its heartbeat", "proxy", id)
			continue
		}
		stats := ""
		if blob, err := json.Marshal(hb.Stats); err == nil {
			stats = string(blob)
		}
		if err := s.apps.Store().SetProxyStatus(id, time.Now(), hb.Version, hb.Routes, stats); err != nil {
			slog.Warn("Cannot record a proxy's status", "proxy", id, "error", err)
		}
	}
}

// Routes builds the table from the registry and assigns it a sequence number
// that bumps exactly when the content changes (a hash comparison, so no
// mutation site needs to notify).
func (s *Server) Routes() (*proxyapi.Table, error) {
	apps, err := s.apps.Store().Apps()
	if err != nil {
		return nil, err
	}
	domains, err := s.apps.Store().ActiveDomains()
	if err != nil {
		return nil, err
	}
	routes := make([]proxyapi.Route, 0, len(apps)+len(domains))
	targets := make(map[string]string, len(apps))
	private := make(map[string]bool, len(apps))
	for _, a := range apps {
		addr := s.nodeAddress(a.Host)
		if addr == "" {
			continue // its node has not reported where to reach it yet
		}
		target := fmt.Sprintf("%s:%d", addr, a.Port)
		targets[a.Name] = target
		private[a.Name] = a.Private
		routes = append(routes, proxyapi.Route{Host: a.Name + "." + s.config.BaseDomain, Target: target, Private: a.Private})
	}
	for appName, appDomains := range domains {
		target, ok := targets[appName]
		if !ok {
			continue
		}
		for _, domain := range appDomains {
			// A private app is private on every name it answers to; a custom
			// domain left public would be the URL its owner actually shared.
			routes = append(routes, proxyapi.Route{Host: domain, Target: target, Private: private[appName]})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Host < routes[j].Host })

	// Hash the content; a changed hash bumps the seq.
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
		// Persisted, not just in memory: a proxy keeps the last table it stored
		// across ITS restarts, so a control that restarted its counter would hand
		// out a number the proxy already holds -- and the proxy would discard the
		// push as old while serving a stale table.
		if err := s.apps.Store().SetSetting(store.SettingRoutesSeq, fmt.Sprintf("%d", s.routesSeq)); err != nil {
			slog.Warn("Cannot persist the routing table version", "error", err)
		}
	}
	return &proxyapi.Table{Seq: s.routesSeq, Routes: routes}, nil
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

// CertFor hands a proxy the certificate for one SNI name, through the exact
// combined lookup control's own TLS would use -- including on-demand issuance
// for a not-yet-seen custom domain. Nodes never see keys; proxies must, since
// they terminate. It implements proxyapi.ControlSink.
func (s *Server) CertFor(sni string) (*proxyapi.CertMaterial, error) {
	if s.tlsGetCert == nil {
		return nil, errTLSNotManaged
	}
	// A non-nil Conn: certmagic inspects the hello's connection addresses.
	cert, err := s.tlsGetCert(&tls.ClientHelloInfo{ServerName: sni, Conn: internalHelloConn{}})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", proxyapi.ErrNoCert, sni)
	}
	var chain bytes.Buffer
	for _, der := range cert.Certificate {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return nil, err
	}
	var key bytes.Buffer
	_ = pem.Encode(&key, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return &proxyapi.CertMaterial{CertPEM: chain.String(), KeyPEM: key.String()}, nil
}

// internalHelloConn is the synthetic connection behind a proxy's certificate
// request -- certmagic dereferences the hello's Conn for addresses, so it must
// not be nil.
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
