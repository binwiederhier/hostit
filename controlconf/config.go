// Package controlconf is hostit-control's configuration and its YAML loading.
// It is the control plane's own file, the way nodeconf is a node's and app is
// the tenant's -- three configs, each named for whose it is, so no call site
// has to work out which one a bare "config" meant.
package controlconf

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"heckel.io/hostit/cluster"
)

// AppPreviewMode selects how the dashboard's app cards are previewed: a live
// iframe (default), a periodic headless-chromium screenshot, or not at all.
// The workspace's own preview panes are always live.
type AppPreviewMode string

const (
	// AppPreviewLive embeds each running app in a scaled-down live iframe.
	AppPreviewLive = AppPreviewMode("live")
	// AppPreviewScreenshot shows a headless-chrome screenshot instead of the
	// live iframe, re-shot periodically and shortly after assistant changes.
	// Shots run in a sandboxed podman container; no host browser needed.
	AppPreviewScreenshot = AppPreviewMode("screenshot")
	// AppPreviewOff drops the card preview entirely.
	AppPreviewOff = AppPreviewMode("off")
)

// AppPreviewIsolationMode selects how the screenshot container's network is
// confined. Strict (the default) lets it reach only the target app's resolved
// IP and the public internet, blocking the host, the LAN/VPC and the cloud
// metadata endpoint; off applies no egress filter.
type AppPreviewIsolationMode string

const (
	// AppPreviewIsolationStrict allows only the app's resolved IP plus the
	// public internet (minus RFC1918/link-local/CGNAT). The default.
	AppPreviewIsolationStrict = AppPreviewIsolationMode("strict")
	// AppPreviewIsolationOff applies no egress filter to shot containers. Only
	// safe on a network the operator fully trusts.
	AppPreviewIsolationOff = AppPreviewIsolationMode("off")
)

// TLSMode determines how the reverse proxy terminates TLS
type TLSMode string

const (
	// TLSLetsEncrypt enables on-demand per-subdomain certificates via ACME (the default)
	TLSLetsEncrypt = TLSMode("letsencrypt")
	// TLSOff disables TLS entirely; the proxy serves plain HTTP (dev, or behind another proxy)
	TLSOff = TLSMode("off")
)

const (
	// Each component owns a directory under /etc/hostit and reads its own file
	// there: control's registry/web settings and a node's identity have almost
	// nothing in common, and a remote node must not carry control's config at
	// all. hostit-proxy's default lives in the proxy package, in the same shape.
	DefaultControlConfigFile = "/etc/hostit/control/control.yml"
	// LegacyServerConfigFile is the pre-split shared file. Installs that still
	// have it keep working (see ResolveConfigFile), so upgrading the package
	// does not strand a running daemon.
	LegacyServerConfigFile = "/etc/hostit/server.yml"
	// DefaultSocketFile is the APP socket: the in-container CLI dials it, and
	// since the relay work it is served by hostit-node on every host. Control
	// keeps the path in its config only to hand it to things that name it (the
	// assistant sandbox mounts its directory).
	DefaultSocketFile = "/run/hostit/hostit.sock"
	// DefaultControlSocketFile is control's OWN unix socket: the operator CLI
	// (peer uid 0 = admin, no token) and the assistant sandbox, which needs
	// control's full registry to resolve a remote-node app's uid. Distinct from
	// the app socket so a worker node can never serve -- or answer for -- the
	// admin surface.
	DefaultControlSocketFile = "/run/hostit/control.sock"
	// DNSProviderRoute53 enables DNS-01 challenges via AWS Route 53, which is
	// what a wildcard certificate requires (Let's Encrypt does not issue
	// wildcards over HTTP-01)
	DNSProviderRoute53 = "route53"
	// minAdminTokenChars is the floor Validate puts under the admin token: far
	// below any generated token, so only hand-typed weak secrets fail.
	minAdminTokenChars = 16
)

var (
	errBaseDomainRequired = errors.New("base-domain is required, e.g. apps.example.com")
	errAdminTokenRequired = errors.New("admin-token is required; generate one with e.g. openssl rand -hex 24")
)

// Config is the hostit server configuration, loaded from a YAML file (see LoadConfig)
type Config struct {
	BaseDomain string `yaml:"base-domain"` // Apps live at <app>.<base-domain>; requires wildcard DNS
	AdminToken string `yaml:"admin-token"` // Bearer token for the admin REST API
	// ListenHTTP is where hostit-proxy forwards; a local address, since the
	// proxy owns :443 in every deployment and control never binds it.
	ListenHTTP        string  `yaml:"listen-http"`         // HTTP listener (ACME challenges + redirect, or plain proxy if TLS off)
	ListenAPI         string  `yaml:"listen-api"`          // Optional extra plain-HTTP admin API listener, e.g. 127.0.0.1:2900
	SocketFile        string  `yaml:"socket-file"`         // The app socket (served by hostit-node; control only names it)
	ControlSocketFile string  `yaml:"control-socket-file"` // Control's own socket: operator CLI + assistant sandbox
	DataDir           string  `yaml:"data-dir"`            // SQLite registry + ACME certs
	AppsDir           string  `yaml:"apps-dir"`            // Home directories of app users
	APIHost           string  `yaml:"api-host"`            // Hostname routed to the admin API; defaults to <base-domain>
	SSHHost           string  `yaml:"ssh-host"`            // Hostname reported for SSH access; defaults to base-domain
	TLS               TLSMode `yaml:"tls"`                 // "letsencrypt" or "off"
	LetsEncryptEmail  string  `yaml:"letsencrypt-email"`   // Optional contact email for ACME

	// Wildcard TLS: with a DNS provider configured, hostit obtains ONE wildcard
	// certificate for *.<base-domain> instead of a certificate per app. New apps
	// then serve TLS immediately, and unknown hostnames reach the proxy (and its
	// 404 page) instead of failing the TLS handshake.
	DNSProvider     string `yaml:"dns-provider"`      // "route53" or empty (per-app on-demand certs)
	AWSRegion       string `yaml:"aws-region"`        // Optional; falls back to AWS_REGION
	AWSAccessKeyID  string `yaml:"aws-access-key-id"` // Optional; falls back to the usual AWS env/instance credentials
	AWSSecretKey    string `yaml:"aws-secret-key"`    // Optional; see above
	AWSHostedZoneID string `yaml:"aws-hosted-zone-id"`

	// Web app and user accounts
	GoogleClientID     string `yaml:"google-client-id"`     // Google OAuth client ID; empty disables the web login
	GoogleClientSecret string `yaml:"google-client-secret"` // Google OAuth client secret

	// ConnectionClients are the OAuth clients this instance holds for each
	// connectable provider, keyed by provider name (slack, discord, github,
	// jira, ...). A provider with no client is simply not offered -- there is
	// no shared hostit client to inherit, because registering one and getting
	// it reviewed is the operator's own relationship with the provider.
	ConnectionClients map[string]OAuthClient `yaml:"connections"`
	SessionKey        string                 `yaml:"session-key"`  // Secret for signing session cookies; generated if empty
	AdminEmails       []string               `yaml:"admin-emails"` // These emails become active admins on first login
	Breakglass        bool                   `yaml:"breakglass"`   // Allow the admin token to mint a session for an admin email (no Google); for e2e/recovery
	// ListenCluster is where members on OTHER machines dial in: mTLS, with
	// per-member certificates from the cluster CA. Empty on a single-box
	// install, which admits no remote members at all.
	//
	// It admits nodes AND proxies, which is why it is not called listen-node
	// any more; that name is still read (see ListenNode) so an existing config
	// keeps working.
	ListenCluster string `yaml:"listen-cluster"`
	// ListenNode is the retired name for ListenCluster, from when only nodes
	// dialed in. Honored so an existing deployment keeps connecting.
	ListenNode string `yaml:"listen-node"`

	// ClusterSocket is where members sharing this host dial in. Always present,
	// and needing no credentials: the socket is root-only and the kernel
	// identifies the caller.
	ClusterSocket string `yaml:"cluster-socket"`

	// ClusterCertFile/ClusterKeyFile are CONTROL's cluster identity: the mTLS
	// certificate its node listener presents (CN "control"), and
	// ClusterCACertFile is the CA every node certificate must chain to. Unset,
	// all three fall back to the set control mints itself under data-dir/ipc,
	// so a single-host deployment needs no configuration. A node's own
	// credentials are in ITS config (node.Config), not here.
	ClusterCertFile   string `yaml:"cluster-cert-file"`
	ClusterKeyFile    string `yaml:"cluster-key-file"`
	ClusterCACertFile string `yaml:"cluster-ca-cert-file"`

	AppPreview           AppPreviewMode          `yaml:"app-preview"`             // "live" (iframe, default), "screenshot" (periodic headless-chromium shots) or "off"
	AppPreviewIsolation  AppPreviewIsolationMode `yaml:"app-preview-isolation"`   // "strict" (default) or "off"; how the shot container's network is confined
	AppPreviewAllowCIDRs []string                `yaml:"app-preview-allow-cidrs"` // Extra destination CIDRs the shot container may reach in strict mode

	// Built-in coding assistant (the in-browser agent). An empty API key disables it.
	AnthropicAPIKey string `yaml:"anthropic-api-key"` // Anthropic API key for the built-in assistant; empty disables it

	// Optional Claude Max backend for the assistant: a subscription OAuth token
	// (from `claude setup-token`) that drives `claude -p` inside a sandbox
	// container instead of the metered API. Empty disables the backend. The token
	// is a high-value personal secret and is only ever mounted into the assistant
	// sandbox container, never an app container (see plans/260810-...).
	ClaudeCodeOAuthToken string `yaml:"claude-code-oauth-token"`
}

// AssistantEnabled reports whether the built-in coding assistant is configured
func (c *Config) AssistantEnabled() bool {
	return c.AnthropicAPIKey != ""
}

// ClaudeBackendEnabled reports whether the optional Claude Max (subscription)
// backend is configured, i.e. a `claude setup-token` OAuth token is present
func (c *Config) ClaudeBackendEnabled() bool {
	return c.ClaudeCodeOAuthToken != ""
}

// AssistantAvailable reports whether the built-in assistant can run at all,
// through either backend, so the UI and routes can enable it. Each backend counts
// as soon as its own credential is present: the API key, or the Claude.ai token.
func (c *Config) AssistantAvailable() bool {
	return c.AssistantEnabled() || c.ClaudeBackendEnabled()
}

// IsAdminEmail reports whether the given address is one of the configured admins
func (c *Config) IsAdminEmail(email string) bool {
	for _, e := range c.AdminEmails {
		if strings.EqualFold(strings.TrimSpace(e), strings.TrimSpace(email)) {
			return true
		}
	}
	return false
}

// NewConfig returns a Config with all defaults set; BaseDomain and AdminToken must be filled in
func NewConfig() *Config {
	return &Config{
		ClusterSocket:       cluster.DefaultSocketFile,
		ListenHTTP:          ":80",
		SocketFile:          DefaultSocketFile,
		ControlSocketFile:   DefaultControlSocketFile,
		DataDir:             "/var/lib/hostit",
		AppsDir:             "/var/lib/hostit/apps",
		TLS:                 TLSLetsEncrypt,
		AppPreview:          AppPreviewLive,
		AppPreviewIsolation: AppPreviewIsolationStrict,
	}
}

// WebEnabled reports whether Google login (and thus the web app) is configured
func (c *Config) WebEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// OAuthClient is one provider's registered client.
type OAuthClient struct {
	ClientID     string `yaml:"client-id"`
	ClientSecret string `yaml:"client-secret"`

	// The rest describe a provider hostit does NOT ship, written by the
	// operator. A catalog entry was always pure data, so supplying that data
	// here gets the same behaviour with no code -- see connections/custom.go.
	//
	// Setting Label is what marks an entry as describing a provider rather than
	// just holding a client for one hostit already knows.
	Label  string   `yaml:"label"`
	Scopes []string `yaml:"scopes"`
	// Issuer stands in for AuthURL and TokenURL: hostit reads the service's own
	// authorization-server metadata to find them.
	Issuer         string            `yaml:"issuer"`
	AuthURL        string            `yaml:"auth-url"`
	TokenURL       string            `yaml:"token-url"`
	AuthParams     map[string]string `yaml:"auth-params"`
	LongLivedToken bool              `yaml:"long-lived-token"`
	Help           string            `yaml:"help"`
	NameHint       string            `yaml:"name-hint"`
}

// DescribesProvider reports whether this entry defines a provider of its own,
// rather than supplying a client for one hostit ships.
//
// The label is the marker because it is the one field a custom entry cannot do
// without -- it is what a person reads in the Add menu -- and the one a
// client-only entry has no reason to set.
func (c OAuthClient) DescribesProvider() bool {
	return strings.TrimSpace(c.Label) != ""
}

// ConnectionClient returns the OAuth client for a provider, or empties if this
// instance holds none (in which case the provider is not offered).
//
// Google's connections fall back to the LOGIN client: it is the same Google
// Cloud OAuth client, scopes are requested per authorization rather than baked
// into the registration, so an instance that can already sign in with Google
// should not need a second one to read a calendar. An explicit entry wins.
func (c *Config) ConnectionClient(provider string) (clientID, clientSecret string) {
	if client, ok := c.ConnectionClients[provider]; ok && client.ClientID != "" {
		return client.ClientID, client.ClientSecret
	}
	if provider == "google-calendar" || provider == "gmail" {
		return c.GoogleClientID, c.GoogleClientSecret
	}
	return "", ""
}

// RedirectURL is the OAuth callback URL for a login started on the given host.
// Google matches it exactly, so the callback must come back to the hostname the
// user actually visited; every hostname in WebHostnames should be registered.
func (c *Config) RedirectURL(host string) string {
	return c.WebURL(host) + "/auth/callback"
}

// WebURL is the origin of the web app as reached on the given host, falling
// back to the canonical one for a host this instance does not serve the web app
// on. Anything that has to hand out an absolute URL to a third party builds it
// from here, so an instance on any hostname names itself correctly.
func (c *Config) WebURL(host string) string {
	scheme := "https"
	if c.TLS == TLSOff {
		scheme = "http"
	}
	if !c.IsWebHostname(host) {
		host = c.APIHostname()
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// LoadConfig reads a YAML config file on top of the defaults from NewConfig
func LoadConfig(filename string) (*Config, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	c := NewConfig()
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("cannot parse config %s: %w", filename, err)
	}
	if c.ListenCluster == "" {
		c.ListenCluster = c.ListenNode // the retired key
	}
	return c, nil
}

// Validate checks that the config is complete enough to run the server
// ResolveConfigFile picks the config a component should read: its own file
// when it exists, else the legacy shared one when THAT exists (a pre-split
// install), else its own path -- so a missing-file error names the location
// the operator is meant to create, not the retired one.
func ResolveConfigFile(own, legacy string) string {
	if _, err := os.Stat(own); err == nil {
		return own
	}
	if _, err := os.Stat(legacy); err == nil {
		slog.Warn("Reading the pre-split shared config; move it to the component's own file", "file", legacy, "expected", own)
		return legacy
	}
	return own
}

// Validate checks a CONTROL config: the web app, certificates and registry
// settings the control plane cannot run without.
func (c *Config) Validate() error {
	if c.BaseDomain == "" {
		return errBaseDomainRequired
	}
	if c.AdminToken == "" {
		return errAdminTokenRequired
	}
	// The token is compared in constant time, but nothing beats a large token
	// space: a hand-typed secret is brute-forceable over HTTPS at line rate.
	// The floor is far below any generated token, so only weak ones fail.
	if len(c.AdminToken) < minAdminTokenChars {
		return fmt.Errorf("admin-token is too short (%d chars, minimum %d); generate one with e.g. openssl rand -hex 24", len(c.AdminToken), minAdminTokenChars)
	}
	if c.TLS != TLSLetsEncrypt && c.TLS != TLSOff {
		return fmt.Errorf("invalid tls mode %q, must be %q or %q", c.TLS, TLSLetsEncrypt, TLSOff)
	}
	if c.AppPreview != AppPreviewLive && c.AppPreview != AppPreviewScreenshot && c.AppPreview != AppPreviewOff {
		return fmt.Errorf("invalid app-preview mode %q, must be %q, %q or %q", c.AppPreview, AppPreviewLive, AppPreviewScreenshot, AppPreviewOff)
	}
	if c.AppPreviewIsolation != AppPreviewIsolationStrict && c.AppPreviewIsolation != AppPreviewIsolationOff {
		return fmt.Errorf("invalid app-preview-isolation %q, must be %q or %q", c.AppPreviewIsolation, AppPreviewIsolationStrict, AppPreviewIsolationOff)
	}
	for _, cidr := range c.AppPreviewAllowCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid app-preview-allow-cidrs entry %q: %w", cidr, err)
		}
	}
	if c.DNSProvider != "" && c.DNSProvider != DNSProviderRoute53 {
		return fmt.Errorf("invalid dns-provider %q, only %q is supported", c.DNSProvider, DNSProviderRoute53)
	}
	return nil
}

// WildcardTLS reports whether one wildcard certificate covers all apps, rather
// than issuing a certificate per app on first request
func (c *Config) WildcardTLS() bool {
	return c.TLS == TLSLetsEncrypt && c.DNSProvider != ""
}

// CertNames returns the names the wildcard certificate must cover: every app
// subdomain plus the base domain itself
func (c *Config) CertNames() []string {
	return []string{"*." + c.BaseDomain, c.BaseDomain}
}

// APIHostname is the canonical hostname of the web app and API: the base domain
// itself, unless the operator pins another one
func (c *Config) APIHostname() string {
	if c.APIHost != "" {
		return c.APIHost
	}
	return c.BaseDomain
}

// WebHostnames are all hostnames that serve the web app and API. The base
// domain is the front door; "hostit.<base>" stays valid so links, prompts and
// OAuth redirects handed out earlier keep working.
func (c *Config) WebHostnames() []string {
	hosts := []string{c.APIHostname()}
	for _, host := range []string{c.BaseDomain, "hostit." + c.BaseDomain} {
		if host != c.APIHostname() {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// IsWebHostname reports whether a hostname serves the web app and API
func (c *Config) IsWebHostname(host string) bool {
	for _, candidate := range c.WebHostnames() {
		if host == candidate {
			return true
		}
	}
	return false
}

// SSHHostname returns the hostname reported to clients for SSH access
func (c *Config) SSHHostname() string {
	if c.SSHHost != "" {
		return c.SSHHost
	}
	return c.BaseDomain
}
