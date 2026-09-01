// Package control is hostit's control plane: the admin REST API and web app,
// the peercred-authenticated unix socket for the app-side CLI, the
// TLS-terminating reverse proxy that routes <app>.<base-domain> to each app's
// loopback port, and the Manager that orchestrates app lifecycles -- creation
// and deletion, placement, port allocation, ownership and the registry of
// record. Machine work (subvolumes, unix users, containers, firewall, files,
// state) lives in package node: the Manager holds no machine of its own and
// drives the app's node through the nodeapi verbs over the cluster link -- even
// a colocated node is a separate hostit-node process, never in-process.
package control

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
	"sync/atomic"
	"time"

	"heckel.io/hostit/proxy/grant"

	"github.com/caddyserver/certmagic"
	"golang.org/x/sync/errgroup"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/connections"
	"heckel.io/hostit/control/preview"
	"heckel.io/hostit/control/user"
	"heckel.io/hostit/metrics"
	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
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
	apps      *Manager
	users     *user.Manager
	assistant *assistant.Manager // nil unless an Anthropic API key is configured
	// assistantOps is the assistant's tool surface, kept so SetNode can repoint
	// its node agent along with the handlers'; nil when no assistant is configured
	assistantOps *appOps
	sessions     *sessionManager
	// grants signs the per-app credential a private app's visitor carries on the
	// app's own hostname, where the session cookie does not reach (appaccess.go).
	grants *grant.Signer
	// connections is credential custody for the accounts and secrets an owner
	// connects; nil only if its key could not be loaded.
	connections *connectionManager
	// mcp holds MCP consents in flight. Separate from connections because a
	// half-finished consent is not a credential: it is browser state with a
	// deadline, and it dies with the process on purpose (control/mcp.go).
	mcp    *mcpBroker
	api    http.Handler
	socket http.Handler
	proxy  http.Handler

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

	// node is the NodeAgent the per-app machine operations go through: the
	// routing agent that resolves each app's host to its connected node.
	node NodeAgent

	// previews schedules dashboard screenshots after assistant changes; nil
	// unless app-preview is "screenshot" (see SetPreviews)
	previews *preview.Manager

	// tlsGetCert is the combined certificate lookup (wildcard + custom
	// domains), captured for the internal cert endpoint; nil until Run wires TLS
	tlsGetCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	// proxies are the connected data-plane proxies control pushes routes to
	proxies *ProxyRegistry

	// routesHash/routesSeq version the routing table (see proxies.go)
	routesHash      string
	routesSeq       int64
	routesMu        sync.Mutex   // Protects routesHash, routesSeq
	limitsMu        sync.Mutex   // Serializes limit updates so the pool-fit check and apply are atomic
	activeTerminals atomic.Int32 // Count of open terminal sessions, capped to protect the node

	domainMu sync.RWMutex // Protects domainCache and issuing
}

// New creates a Server; it does not start any listeners
func New(conf *config.Config, apps *Manager, users *user.Manager) *Server {
	s := &Server{
		config:         conf,
		apps:           apps,
		node:           apps.NodeAgent(),
		users:          users,
		sessions:       newSessionManager(conf.SessionKey),
		grants:         grant.NewSigner(conf.SessionKey, appGrantTTL),
		usernameForUID: usernameForUID,
		proxies:        NewProxyRegistry(),
		mcp:            newMCPBroker(),
	}
	s.exchangeGoogleCode = s.exchangeGoogleCodeLive
	// Connections reuse the instance's Google OAuth client and come back on the
	// same /auth/callback the login uses, told apart by the state parameter --
	// so connecting an account needs no second redirect URI registered with the
	// provider. Each provider's own OAuth client comes from the instance's
	// config (connections: in control.yml); one with no client is not offered.
	if key, err := connections.LoadOrCreateKey(conf.DataDir); err != nil {
		slog.Warn("Connections disabled: cannot load the credential key", "error", err)
	} else {
		s.connections = newConnectionManager(apps.Store(), key, conf)
		// An operator's own providers, from connections: in control.yml. A
		// malformed entry is fatal on purpose: it is the file they just edited,
		// and a provider silently missing from a menu is the hardest possible
		// way to find out it is wrong.
		if err := s.connections.loadCustomProviders(conf); err != nil {
			slog.Error("Cannot load the custom connection providers from control.yml", "error", err)
			os.Exit(1)
		}
	}
	// The Manager builds the desired state control asserts on nodes; the
	// per-app keys and limits in it need the user tables, which live here.
	apps.SetPolicy(&serverPolicy{s})
	// Continue the routing table's version where the last control process left
	// off (see Routes); a fresh counter would collide with what a
	// running proxy already holds.
	if settings, err := apps.Store().Settings(); err == nil {
		if seq, err := strconv.ParseInt(settings[store.SettingRoutesSeq], 10, 64); err == nil {
			s.routesSeq = seq
		}
	}
	// The built-in coding assistant, available when either backend is configured:
	// the metered Anthropic API (an API key) or the operator's Claude Max
	// subscription (a sandboxed claude -p). Its tools are the app's own operations,
	// so it is confined to one app the way an agent token is.
	if conf.AssistantAvailable() {
		s.assistantOps = &appOps{apps: apps, node: apps.NodeAgent(), changed: s.assistantChanged, server: s}
		s.assistant = assistant.NewManager(assistant.NewClient(conf.AnthropicAPIKey), s.assistantOps, &appTranscripts{store: apps.Store()}, credentials(conf))
		// Wire the Claude Max (subscription) backend whenever its token is configured,
		// so selecting "Claude.ai" actually uses the subscription. Its presence is the
		// whole switch; there is no separate backend setting. (Previously the option
		// could be offered while the backend was unwired, silently running the API
		// model and badging replies as Sonnet with no explanation.)
		if conf.ClaudeBackendEnabled() {
			// The sandbox runs on the app's node now (it has podman, the app's unix
			// user, and the app socket); control holds only the subscription token
			// and passes it down per turn. Routing to the right node -- including an
			// app on another node -- is the node agent's job.
			s.assistant.SetClaudeRunner(&claudeBackend{node: apps.NodeAgent(), token: conf.ClaudeCodeOAuthToken})
			slog.Info("Claude Max (subscription) backend available for the assistant")
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
	g := &errgroup.Group{}

	// Optional Prometheus metrics on a separate interface: instrument the API
	// handler, wire the fleet gauges, and start the /metrics listener.
	if s.config.ListenMetrics != "" {
		s.api = instrumentHTTP(s.api)
		s.registerMetrics()
		ms, err := metrics.Serve(s.config.ListenMetrics)
		if err != nil {
			return fmt.Errorf("metrics listener on %s: %w", s.config.ListenMetrics, err)
		}
		s.servers = append(s.servers, ms)
	}

	// Materialize the SSH relay files at boot so the relay shell has current
	// routes even before any node heartbeat (a no-op unless the relay is on).
	s.apps.refreshSSHRelay()

	// Keep OAuth connections alive without the owner re-authorizing: refresh
	// tokens proactively before they expire, and flag any the provider rejects.
	if s.connections != nil {
		g.Go(func() error {
			s.connections.RefreshLoop(connectionRefreshInterval)
			return nil
		})
	}

	// Unix socket for the app-side CLI ("hostit up" etc.)
	socketListener, err := s.listenSocket()
	if err != nil {
		return err
	}
	socketServer := &http.Server{Handler: s.socket, ConnContext: socketConnContext, ReadHeaderTimeout: readHeaderTimeout}
	s.servers = append(s.servers, socketServer)
	g.Go(func() error {
		slog.Info("Listening on unix socket", "socket", config.DefaultControlSocketFile)
		return ignoreServerClosed(socketServer.Serve(socketListener))
	})

	// Optional loopback admin-API listener (listen-api): the API with no hostname
	// routing, for local development (the web dev server proxies to it) and for
	// scripts/e2e that curl the API directly.
	if s.config.ListenAPI != "" {
		apiServer := &http.Server{Addr: s.config.ListenAPI, Handler: s.api, ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, apiServer)
		g.Go(func() error {
			slog.Info("Listening for admin API", "addr", s.config.ListenAPI)
			return ignoreServerClosed(apiServer.ListenAndServe())
		})
	}

	// hostit-proxy terminates TLS and forwards here, always: it is a component
	// of every deployment, so control serves plain HTTP on a local address and
	// never binds :443. It still MANAGES the certificates -- certmagic lives
	// here, and the proxy asks for material over the cluster link.
	//
	// tls: off skips certificate management entirely, for development.
	if s.config.TLS == config.TLSOff {
		httpServer := &http.Server{Addr: s.config.ListenHTTP, Handler: s.proxy, ReadHeaderTimeout: readHeaderTimeout}
		s.servers = append(s.servers, httpServer)
		g.Go(func() error {
			slog.Info("Listening for HTTP (TLS off)", "addr", s.config.ListenHTTP)
			return ignoreServerClosed(httpServer.ListenAndServe())
		})
		return g.Wait()
	}
	_, issuer, err := s.setupCertmagic()
	if err != nil {
		return err
	}
	// ACME HTTP-01 challenges for custom domains arrive here through the proxy's
	// :80 pass-through, so the challenge middleware wraps everything.
	httpServer := &http.Server{Addr: s.config.ListenHTTP, Handler: issuer.HTTPChallengeHandler(s.proxy), ReadHeaderTimeout: readHeaderTimeout}
	s.servers = append(s.servers, httpServer)
	g.Go(func() error {
		slog.Info("Listening for HTTP behind hostit-proxy", "addr", s.config.ListenHTTP)
		return ignoreServerClosed(httpServer.ListenAndServe())
	})
	return g.Wait()
}

// Stop gracefully shuts down all listeners
func (s *Server) Stop() {
	if s.connections != nil {
		s.connections.StopRefresh()
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range s.servers {
		_ = srv.Shutdown(ctx)
	}
}

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
	// CertFor hands this exact lookup to hostit-proxy, so the data plane serves
	// the same certificates control manages. It uses the context-taking
	// variants: the caller synthesizes the ClientHelloInfo, and certmagic
	// dereferences hello.Conn/ctx on the plain GetCertificate path.
	s.tlsGetCert = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		ctx, cancel := context.WithTimeout(context.Background(), certTimeout)
		defer cancel()
		if cert, err := magic.GetCertificateWithContext(ctx, hello); err == nil {
			return cert, nil
		}
		return s.domainMagic.GetCertificateWithContext(ctx, hello)
	}
	return tlsConfig, issuer, nil
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
	if err := os.MkdirAll(filepath.Dir(config.DefaultControlSocketFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(config.DefaultControlSocketFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", config.DefaultControlSocketFile)
	if err != nil {
		return nil, err
	}
	// World-connectable on purpose: authorization happens via SO_PEERCRED
	if err := os.Chmod(config.DefaultControlSocketFile, 0o666); err != nil {
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
	// One grouped query for the whole batch: a list of apps must not cost a
	// count per app just to label one of them "Restricted".
	viewers, err := s.apps.Store().ViewerCounts()
	if err != nil {
		slog.Warn("Cannot read viewer counts", "error", err)
	}
	for _, r := range resp {
		r.ViewerCount = viewers[r.ID]
		state := states[r.Name]
		r.Running, r.AppRunning, r.MemoryMB = state.Running, state.AppRunning, state.MemoryMB
		r.AppState = state.AppState
		r.CPUPercent = state.CPUPercent
		r.StartedAt, r.AppStartedAt = state.StartedAt, state.AppStartedAt
		r.MemoryLimit, r.DiskLimit, r.CPUMilli = s.appLimits(r.Name)
	}
	return resp
}

// appLimits returns the EFFECTIVE caps that apply to an app: its own
// admin-set overrides where present, else the owner's defaults. CPU has no
// owner default, so the override is the whole story (0 = uncapped).
func (s *Server) appLimits(name string) (memoryMB, diskMB, cpuMilli int) {
	a, err := s.apps.App(name)
	if err != nil {
		return 0, 0, 0
	}
	limits, err := s.users.Defaults()
	if err != nil {
		return 0, 0, 0
	}
	if a.OwnerID != "" {
		if owner, err := s.users.User(a.OwnerID); err == nil {
			if ownerLimits, err := s.users.Limits(owner); err == nil {
				limits = ownerLimits
			}
		}
	}
	memoryMB, diskMB, cpuMilli = user.EffectiveAppLimits(limits, a)
	return memoryMB, node.EffectiveDiskCapMB(diskMB), cpuMilli
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
func (s *Server) SetNode(node NodeAgent) {
	s.node = node
	// The assistant's tools act through their own reference; repoint it too, or
	// external-backend turns keep operating on control's LOCAL machine for apps
	// hosted on other nodes (deploys build against the wrong paths).
	if s.assistantOps != nil {
		s.assistantOps.node = node
	}
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

// sshHostFor returns the SSH hostname to advertise for an app on the given node:
// the node's own reported host, falling back to control's base domain.
func (s *Server) sshHostFor(nodeID string) string {
	if nodeID != "" {
		if n, err := s.apps.Store().Node(nodeID); err == nil && n.SSHHost != "" {
			return n.SSHHost
		}
	}
	return s.config.SSHHostname()
}

// sshInfoFor builds the SSH access block advertised for an app.
func sshInfoFor(user, host string) apiSSHInfo {
	return apiSSHInfo{User: user, Host: host, Command: fmt.Sprintf("ssh %s@%s", user, host)}
}

func (s *Server) appResponse(a *store.App, customDomain string) *apiAppResponse {
	ownerEmail, ownerName := s.ownerIdentity(a.OwnerID)
	resp := &apiAppResponse{
		ID:               a.ID,
		Name:             a.Name,
		URL:              s.apps.URL(a),
		Port:             a.Port,
		Host:             hostOrLocal(a.Host),
		DiskMB:           a.DiskMB,
		OwnerEmail:       ownerEmail,
		OwnerName:        ownerName,
		SnapshotsEnabled: true, // btrfs is mandatory, so snapshots are always available
		PreviewMode:      string(s.config.AppPreview),
		AssistantEnabled: s.assistant != nil,
		// What the app says it is, straight from its hostit.yml; empty for a stub
		Description:    s.apps.Description(a.Name),
		Snapshot:       s.snapshotConfigFor(a.Name),
		Archived:       a.Archived,
		Private:        a.Private,
		Tabs:           a.Tabs,
		CreatedAt:      a.CreatedAt,
		LimitOverrides: apiLimitOverrides{MemoryMB: a.MemoryLimitMB, DiskMB: a.DiskLimitMB, CPUMilli: a.CPUMilli},
		SSH:            sshInfoFor(a.Name, s.sshHostFor(a.Host)),
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

// ignoreServerClosed maps the expected shutdown error to nil
func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
