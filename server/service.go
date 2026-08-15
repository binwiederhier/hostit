// Package server implements the hostit daemon: the admin REST API, the peercred-
// authenticated unix socket for the app-side CLI, and the TLS-terminating reverse
// proxy that routes <app>.<base-domain> to each app's loopback port.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	osuser "os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/sync/errgroup"
	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/config"
	"heckel.io/hostit/preview"
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

	// magic manages the wildcard / app-subdomain certificates. domainMagic manages
	// custom-domain certificates: in wildcard (DNS-01) mode it is a separate config
	// that delegates the ACME challenge to a name in our own zone (OverrideDomain),
	// so a cert issues for a zone we do not control and even when the box is not
	// publicly reachable; otherwise it is the same on-demand config as magic. Both
	// are nil when TLS is off, in which case custom domains route over plain HTTP.
	magic       *certmagic.Config
	domainMagic *certmagic.Config
	// domainCache maps an active custom domain to its app, for the proxy; rebuilt
	// from the store on change. nil until first loaded. issuing tracks domains with
	// an in-flight certificate attempt, so the retry loop does not pile on.
	domainCache map[string]string
	issuing     map[string]bool

	servers []*http.Server // Running HTTP servers, for Stop

	// node is the NodeAgent the per-app machine operations go through: today
	// always the in-process Manager ("local"); the multi-node split resolves an
	// app's host to a remote agent here instead.
	node app.NodeAgent

	// previews schedules dashboard screenshots after assistant changes; nil
	// unless app-preview is "screenshot" (see SetPreviews)
	previews *preview.Manager

	// tlsGetCert is the combined certificate lookup (wildcard + custom
	// domains), captured for the internal cert endpoint; nil until Run wires TLS
	tlsGetCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	// routesHash/routesSeq version the internal routing table (see internal.go)
	routesHash string
	routesSeq  int64
	routesMu   sync.Mutex // Protects routesHash, routesSeq

	domainMu sync.RWMutex // Protects domainCache and issuing
}

// New creates a Server; it does not start any listeners
func New(conf *config.Config, apps *app.Manager, users *user.Manager) *Server {
	s := &Server{
		config:         conf,
		apps:           apps,
		node:           apps,
		users:          users,
		sessions:       newSessionManager(conf.SessionKey),
		usernameForUID: usernameForUID,
	}
	s.exchangeGoogleCode = s.exchangeGoogleCodeLive
	// The built-in coding assistant, available when either backend is configured:
	// the metered Anthropic API (an API key) or the operator's Claude Max
	// subscription (a sandboxed claude -p). Its tools are the app's own operations,
	// so it is confined to one app the way an agent token is.
	if conf.AssistantAvailable() {
		s.assistant = assistant.NewManager(assistant.NewClient(conf.AnthropicAPIKey), &appOps{apps: apps, node: apps, changed: s.assistantChanged}, &appTranscripts{store: apps.Store()}, conf.AssistantModel)
		// Wire the Claude Max (subscription) backend whenever its token is configured,
		// so selecting "Claude.ai" actually uses the subscription. Its presence is the
		// whole switch; there is no separate backend setting. (Previously the option
		// could be offered while the backend was unwired, silently running the API
		// model and badging replies as Sonnet with no explanation.)
		if conf.ClaudeBackendEnabled() {
			sandbox, err := assistant.NewSandbox(conf)
			if err != nil {
				// A missing sandbox disables only the subscription option; the API
				// backend still serves, so never take the whole assistant down here.
				slog.Error("Cannot start the Claude Max assistant backend; using the API only", "error", err)
			} else {
				s.assistant.SetClaudeRunner(&claudeBackend{sandbox: sandbox})
				slog.Info("Claude Max (subscription) backend available for the assistant")
			}
		}
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
	if s.config.ListenInternal != "" {
		if err := s.listenInternal(); err != nil {
			return err
		}
	}
	if s.config.ListenAPI != "" {
		apiServer := &http.Server{Addr: s.config.ListenAPI, Handler: s.api, ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, apiServer)
		g.Go(func() error {
			slog.Info("Listening for admin API", "addr", s.config.ListenAPI)
			return ignoreServerClosed(apiServer.ListenAndServe())
		})
	}

	// Public proxy: behind hostit-proxy (plain HTTP on a local address, cert
	// management still here), TLS via certmagic, or plain HTTP if TLS is off.
	if s.config.BehindProxy {
		_, issuer, err := s.setupCertmagic()
		if err != nil {
			return err
		}
		// The full public handler on the local address hostit-proxy forwards to.
		// ACME HTTP-01 challenges for custom domains arrive here through the
		// proxy's :80 pass-through, so the challenge middleware wraps everything.
		httpServer := &http.Server{Addr: s.config.ListenHTTP, Handler: issuer.HTTPChallengeHandler(s.proxy), ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, httpServer)
		g.Go(func() error {
			slog.Info("Listening for HTTP behind hostit-proxy", "addr", s.config.ListenHTTP)
			return ignoreServerClosed(httpServer.ListenAndServe())
		})
		return g.Wait()
	}
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
// setupCertmagic wires certificate management (wildcard and/or on-demand,
// custom domains, the combined lookup the internal cert endpoint hands to
// hostit-proxy) WITHOUT starting any public listener -- shared by the
// standalone TLS mode and the behind-proxy mode, where hostit-proxy
// terminates with the material control manages here.
func (s *Server) setupCertmagic() (*tls.Config, *certmagic.ACMEIssuer, error) {
	certmagic.Default.Storage = &certmagic.FileStorage{Path: filepath.Join(s.config.DataDir, "certs")}
	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = s.config.LetsEncryptEmail
	if s.config.WildcardTLS() {
		solver, err := dnsSolver(s.config)
		if err != nil {
			return nil, nil, err
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
	s.magic = magic

	// Custom domains. In wildcard/DNS-01 mode, build a separate config on its own
	// cache whose solver delegates every challenge to a fixed name in our zone
	// (OverrideDomain); the owner CNAMEs their _acme-challenge to it. Kept apart so
	// the wildcard path above is untouched. Otherwise custom domains just reuse the
	// on-demand (HTTP-01) config.
	if s.config.WildcardTLS() {
		domainSolver, err := dnsSolver(s.config)
		if err != nil {
			return nil, nil, err
		}
		domainSolver.OverrideDomain = s.domainChallengeName()
		domainACME := certmagic.DefaultACME
		domainACME.DNS01Solver = domainSolver
		var domainMagic *certmagic.Config
		domainCache := certmagic.NewCache(certmagic.CacheOptions{
			GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return domainMagic, nil },
		})
		domainMagic = certmagic.New(domainCache, certmagic.Default)
		domainMagic.Issuers = []certmagic.Issuer{certmagic.NewACMEIssuer(domainMagic, domainACME)}
		s.domainMagic = domainMagic
	} else {
		s.domainMagic = magic
	}

	// Obtain certificates for existing active custom domains up front, so they
	// serve immediately after a restart and renew in the background.
	s.manageExistingDomains()

	// The wildcard certificate is managed up front (and renewed in the
	// background); on-demand certificates need no such call
	if s.config.WildcardTLS() {
		names := s.config.CertNames()
		slog.Info("Managing wildcard certificate", "names", names, "dns_provider", s.config.DNSProvider)
		if err := magic.ManageAsync(context.Background(), names); err != nil {
			return nil, nil, fmt.Errorf("cannot manage wildcard certificate: %w", err)
		}
	}
	tlsConfig := magic.TLSConfig()
	tlsConfig.NextProtos = append([]string{"h2", "http/1.1"}, tlsConfig.NextProtos...)
	// Serve custom-domain certificates (in a separate cache) by falling back to
	// their config when the base cache has no match for the SNI name.
	if s.domainMagic != nil && s.domainMagic != magic {
		baseGetCert := tlsConfig.GetCertificate
		domainGetCert := s.domainMagic.TLSConfig().GetCertificate
		tlsConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if cert, err := baseGetCert(hello); err == nil {
				return cert, nil
			}
			return domainGetCert(hello)
		}
	}
	// The internal cert endpoint hands this exact lookup to hostit-proxy, so
	// the data plane serves the same certificates control manages. It uses the
	// context-taking variants: the endpoint synthesizes the ClientHelloInfo,
	// and certmagic dereferences hello.Conn/ctx on the plain GetCertificate path.
	s.tlsGetCert = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		ctx, cancel := context.WithTimeout(context.Background(), internalCertTimeout)
		defer cancel()
		if cert, err := magic.GetCertificateWithContext(ctx, hello); err == nil {
			return cert, nil
		}
		return s.domainMagic.GetCertificateWithContext(ctx, hello)
	}
	return tlsConfig, issuer, nil
}

// runTLSServers is the standalone mode: control terminates TLS itself.
func (s *Server) runTLSServers(g *errgroup.Group) error {
	tlsConfig, issuer, err := s.setupCertmagic()
	if err != nil {
		return err
	}

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
	if appName, ok := s.appNameFromHost(name); ok {
		if _, err := s.apps.App(appName); err != nil {
			return fmt.Errorf("no app for host %s", name)
		}
		return nil
	}
	// A registered custom domain (pending or active) may also get a certificate.
	if _, err := s.apps.Store().Domain(name); err == nil {
		return nil
	}
	return fmt.Errorf("host %s is not a registered app or custom domain", name)
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
		r.AppState = state.AppState
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
// firstActiveDomain returns an app's first verified custom domain, or "". Used by
// the single-app endpoints; the list endpoint batches this via store.ActiveDomains.
func (s *Server) firstActiveDomain(name string) string {
	domains, err := s.apps.Store().Domains(name)
	if err != nil {
		return ""
	}
	for _, d := range domains {
		if d.Status == store.DomainActive {
			return d.Domain
		}
	}
	return ""
}

// appResponseFor is appResponse plus the caller-dependent bits (IsOwner), for
// the authenticated API surface; the unix-socket self API keeps plain
// appResponse (the container is the app, ownership does not apply there).
// SetNode points the per-app machine operations at a (remote) NodeAgent:
// what split mode calls when a hostit-node dials in.
func (s *Server) SetNode(node app.NodeAgent) {
	s.node = node
}

// SetPreviews wires the screenshot manager (app-preview: screenshot), so
// assistant changes schedule a debounced dashboard shot.
func (s *Server) SetPreviews(previews *preview.Manager) {
	s.previews = previews
}

// assistantChanged records that the assistant just modified the app; with
// screenshot previews on, that arms the app's debounced shot.
func (s *Server) assistantChanged(name string) {
	if s.previews == nil {
		return
	}
	s.previews.Schedule(name)
}

func (s *Server) appResponseFor(c *caller, a *store.App, customDomain string) *apiAppResponse {
	resp := s.appResponse(a, customDomain)
	resp.IsOwner = c.isAdmin() || a.OwnerID == c.userID()
	return resp
}

func (s *Server) appResponse(a *store.App, customDomain string) *apiAppResponse {
	ownerEmail, ownerName := s.ownerIdentity(a.OwnerID)
	resp := &apiAppResponse{
		ID:               a.ID,
		Name:             a.Name,
		URL:              s.apps.URL(a),
		Port:             a.Port,
		DiskMB:           a.DiskMB,
		OwnerEmail:       ownerEmail,
		OwnerName:        ownerName,
		SnapshotsEnabled: true, // btrfs is mandatory, so snapshots are always available
		PreviewMode:      string(s.config.AppPreview),
		AssistantEnabled: s.assistant != nil,
		// What the app says it is, straight from its hostit.yml; empty for a stub
		Description: s.apps.Description(a.Name),
		CreatedAt:   a.CreatedAt,
		SSH: apiSSHInfo{
			User:    a.Name,
			Host:    s.config.SSHHostname(),
			Command: fmt.Sprintf("ssh %s@%s", a.Name, s.config.SSHHostname()),
		},
	}
	// The first verified custom domain becomes the app's primary public URL. The
	// caller looks it up (one query for a single app, batched for the list).
	resp.CustomDomain = customDomain
	return resp
}

// ownerIdentity resolves an owner ID to an email and name for display; unowned
// apps (created before user accounts existed, or via the global admin token)
// return empties
func (s *Server) ownerIdentity(ownerID string) (email, name string) {
	if ownerID == "" {
		return "", ""
	}
	u, err := s.users.User(ownerID)
	if err != nil {
		return "", ""
	}
	return u.Email, u.Name
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
