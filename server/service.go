// Package server implements the hostit daemon: the admin REST API, the peercred-
// authenticated unix socket for the app-side CLI, and the TLS-terminating reverse
// proxy that routes <app>.<base-domain> to each app's loopback port.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/sync/errgroup"
	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// readHeaderTimeout bounds header reads on all listeners; there are deliberately
	// no read/write timeouts, since proxied apps may serve SSE or websockets
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout is how long Stop waits for in-flight requests
	shutdownTimeout = 5 * time.Second
)

// Server is the hostit daemon; create with New, run with Run
type Server struct {
	config    *config.Config
	apps      *app.Manager
	users     *user.Manager
	assistant *assistant.Manager // nil unless an Anthropic API key is configured
	sessions  *sessionManager
	api       http.Handler
	socket    http.Handler
	proxy     http.Handler

	// usernameForUID maps a peer-credential UID to a username; overridden in tests
	usernameForUID func(uid int) (string, error)
	// exchangeGoogleCode trades an OAuth code for an identity; overridden in tests
	exchangeGoogleCode func(code, host string) (*googleIdentity, error)

	servers []*http.Server // Running HTTP servers, for Stop
}

// New creates a Server; it does not start any listeners
func New(conf *config.Config, apps *app.Manager, users *user.Manager) *Server {
	s := &Server{
		config:         conf,
		apps:           apps,
		users:          users,
		sessions:       newSessionManager(conf.SessionKey),
		usernameForUID: usernameForUID,
	}
	s.exchangeGoogleCode = s.exchangeGoogleCodeLive
	// The built-in coding assistant, if an API key is configured. Its tools are the
	// app's own operations, so it is confined to one app the way an agent token is.
	if conf.AssistantEnabled() {
		s.assistant = assistant.NewManager(assistant.NewClient(conf.AnthropicAPIKey), &appOps{apps: apps}, &appTranscripts{store: apps.Store()}, conf.AssistantModel)
	}
	// The web app and REST API get the full header set (CSP, framing denial) plus
	// the base headers; the public proxy gets only the base headers, so proxied
	// tenant apps are not straitjacketed by our CSP.
	s.api = s.withWebSecurityHeaders(s.withBaseSecurityHeaders(s.newAPIHandler()))
	s.socket = s.newSocketHandler()
	s.proxy = s.withBaseSecurityHeaders(s.newProxyHandler())
	return s
}

// Run starts all listeners and blocks until the first one fails
func (s *Server) Run() error {
	s.apps.ReconcilePortRules() // Registry is the source of truth for port rules
	g := &errgroup.Group{}

	// Unix socket for the app-side CLI ("hostit up" etc.)
	socketListener, err := s.listenSocket()
	if err != nil {
		return err
	}
	socketServer := &http.Server{Handler: s.socket, ConnContext: socketConnContext, ReadHeaderTimeout: readHeaderTimeout}
	s.servers = append(s.servers, socketServer)
	g.Go(func() error {
		slog.Info("Listening on unix socket", "socket", s.config.SocketFile)
		return ignoreServerClosed(socketServer.Serve(socketListener))
	})

	// Optional plain-HTTP admin API listener (e.g. 127.0.0.1:2900)
	if s.config.ListenAPI != "" {
		apiServer := &http.Server{Addr: s.config.ListenAPI, Handler: s.api, ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, apiServer)
		g.Go(func() error {
			slog.Info("Listening for admin API", "addr", s.config.ListenAPI)
			return ignoreServerClosed(apiServer.ListenAndServe())
		})
	}

	// Public proxy: TLS via certmagic, or plain HTTP if TLS is off
	if s.config.TLS == config.TLSOff {
		httpServer := &http.Server{Addr: s.config.ListenHTTP, Handler: s.proxy, ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, httpServer)
		g.Go(func() error {
			slog.Info("Listening for HTTP (TLS off)", "addr", s.config.ListenHTTP)
			return ignoreServerClosed(httpServer.ListenAndServe())
		})
	} else {
		if err := s.runTLSServers(g); err != nil {
			return err
		}
	}
	return g.Wait()
}

// Stop gracefully shuts down all listeners
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range s.servers {
		_ = srv.Shutdown(ctx)
	}
}

// runTLSServers starts the HTTPS proxy with Let's Encrypt certificates, plus the
// HTTP listener that answers ACME challenges and redirects to HTTPS. With a DNS
// provider configured, one wildcard certificate covers every app; otherwise each
// app's certificate is issued on demand on its first request.
func (s *Server) runTLSServers(g *errgroup.Group) error {
	certmagic.Default.Storage = &certmagic.FileStorage{Path: filepath.Join(s.config.DataDir, "certs")}
	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = s.config.LetsEncryptEmail
	if s.config.WildcardTLS() {
		solver, err := dnsSolver(s.config)
		if err != nil {
			return err
		}
		certmagic.DefaultACME.DNS01Solver = solver
		certmagic.DefaultACME.DisableHTTPChallenge = true
		certmagic.DefaultACME.DisableTLSALPNChallenge = true
	} else {
		certmagic.Default.OnDemand = &certmagic.OnDemandConfig{
			DecisionFunc: func(ctx context.Context, name string) error {
				return s.allowTLSHost(name)
			},
		}
	}
	magic := certmagic.NewDefault()
	issuer := certmagic.NewACMEIssuer(magic, certmagic.DefaultACME)
	magic.Issuers = []certmagic.Issuer{issuer}

	// The wildcard certificate is managed up front (and renewed in the
	// background); on-demand certificates need no such call
	if s.config.WildcardTLS() {
		names := s.config.CertNames()
		slog.Info("Managing wildcard certificate", "names", names, "dns_provider", s.config.DNSProvider)
		if err := magic.ManageAsync(context.Background(), names); err != nil {
			return fmt.Errorf("cannot manage wildcard certificate: %w", err)
		}
	}
	tlsConfig := magic.TLSConfig()
	tlsConfig.NextProtos = append([]string{"h2", "http/1.1"}, tlsConfig.NextProtos...)

	// HTTP: ACME challenges + redirect everything else to HTTPS
	httpServer := &http.Server{
		Addr:              s.config.ListenHTTP,
		Handler:           issuer.HTTPChallengeHandler(http.HandlerFunc(redirectHTTPS)),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	s.servers = append(s.servers, httpServer)
	g.Go(func() error {
		slog.Info("Listening for HTTP (ACME + redirect)", "addr", s.config.ListenHTTP)
		return ignoreServerClosed(httpServer.ListenAndServe())
	})

	// HTTPS: the actual proxy
	httpsServer := &http.Server{
		Addr:              s.config.ListenHTTPS,
		Handler:           s.proxy,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	s.servers = append(s.servers, httpsServer)
	g.Go(func() error {
		slog.Info("Listening for HTTPS", "addr", s.config.ListenHTTPS)
		return ignoreServerClosed(httpsServer.ListenAndServeTLS("", ""))
	})
	return nil
}

// allowTLSHost decides whether certmagic may request a certificate for a hostname;
// only the admin API host and registered apps are allowed
func (s *Server) allowTLSHost(name string) error {
	if s.config.IsWebHostname(name) {
		return nil
	}
	appName, ok := s.appNameFromHost(name)
	if !ok {
		return fmt.Errorf("host %s not below base domain", name)
	}
	if _, err := s.apps.App(appName); err != nil {
		return fmt.Errorf("no app for host %s", name)
	}
	return nil
}

// listenSocket creates the CLI unix socket, replacing any stale socket file
func (s *Server) listenSocket() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(s.config.SocketFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(s.config.SocketFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", s.config.SocketFile)
	if err != nil {
		return nil, err
	}
	// World-connectable on purpose: authorization happens via SO_PEERCRED
	if err := os.Chmod(s.config.SocketFile, 0o666); err != nil {
		return nil, err
	}
	return listener, nil
}

// withState fills in live state (running, memory) and the owner's limits for a
// set of apps; one systemd and one podman call cover them all
func (s *Server) withState(resp []*apiAppResponse) []*apiAppResponse {
	names := make([]string, 0, len(resp))
	for _, r := range resp {
		names = append(names, r.Name)
	}
	states := s.apps.CachedStates(names)
	for _, r := range resp {
		state := states[r.Name]
		r.Running, r.AppRunning, r.MemoryMB = state.Running, state.AppRunning, state.MemoryMB
		r.CPUPercent = state.CPUPercent
		r.StartedAt, r.AppStartedAt = state.StartedAt, state.AppStartedAt
		r.MemoryLimit, r.DiskLimit = s.appLimits(r.Name)
	}
	return resp
}

// appLimits returns the memory and disk caps that apply to an app
func (s *Server) appLimits(name string) (memoryMB int, diskMB int) {
	a, err := s.apps.App(name)
	if err != nil {
		return 0, 0
	}
	limits, err := s.users.Defaults()
	if err != nil {
		return 0, 0
	}
	if a.OwnerID != "" {
		if owner, err := s.users.User(a.OwnerID); err == nil {
			if ownerLimits, err := s.users.Limits(owner); err == nil {
				limits = ownerLimits
			}
		}
	}
	return limits.MemoryMB, limits.DiskMB
}

// appResponse converts an app to its API form
func (s *Server) appResponse(a *store.App) *apiAppResponse {
	resp := &apiAppResponse{
		Name:             a.Name,
		URL:              s.apps.URL(a),
		Port:             a.Port,
		DiskMB:           a.DiskMB,
		OverQuota:        a.OverQuota,
		OwnerEmail:       s.ownerEmail(a.OwnerID),
		SnapshotsEnabled: s.apps.SnapshotsEnabled(),
		// What the app says it is, straight from its hostit.yml; empty for a stub
		Description: s.apps.Description(a.Name),
		CreatedAt:   a.CreatedAt,
		SSH: apiSSHInfo{
			User:    a.Name,
			Host:    s.config.SSHHostname(),
			Command: fmt.Sprintf("ssh %s@%s", a.Name, s.config.SSHHostname()),
		},
	}
	return resp
}

// ownerEmail resolves an owner ID to an email for display; unowned apps (created
// before user accounts existed, or via the global admin token) return empty
func (s *Server) ownerEmail(ownerID string) string {
	if ownerID == "" {
		return ""
	}
	u, err := s.users.User(ownerID)
	if err != nil {
		return ""
	}
	return u.Email
}

// usernameForUID is the production UID-to-username mapping via the user database
func usernameForUID(uid int) (string, error) {
	u, err := osuser.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	target := "https://" + hostOnly(r.Host) + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

// ignoreServerClosed maps the expected shutdown error to nil
func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
