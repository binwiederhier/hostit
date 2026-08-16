// Package config defines the hostit server configuration and its YAML loading logic.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
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
	DefaultNodeConfigFile    = "/etc/hostit/node/node.yml"
	// LegacyServerConfigFile is the pre-split shared file. Installs that still
	// have it keep working (see ResolveConfigFile), so upgrading the package
	// does not strand a running daemon.
	LegacyServerConfigFile = "/etc/hostit/server.yml"
	// DefaultSocketFile is the daemon's app-side unix socket; the in-container
	// CLI dials it to reach the daemon.
	DefaultSocketFile = "/run/hostit/hostit.sock"
	// DNSProviderRoute53 enables DNS-01 challenges via AWS Route 53, which is
	// what a wildcard certificate requires (Let's Encrypt does not issue
	// wildcards over HTTP-01)
	DNSProviderRoute53 = "route53"
	// DefaultAssistantModel is the model the built-in assistant uses unless the
	// operator names another one
	DefaultAssistantModel = "claude-sonnet-5"

	// ExternalClaudeMode is the assistant mode that runs on the operator's Claude
	// Max subscription (the sandboxed claude -p). Every other mode is an API model
	// id (e.g. "claude-sonnet-5"). The UI shows one dropdown: this plus the models.
	ExternalClaudeMode  = "external-claude"
	ExternalClaudeLabel = "Claude.ai"
)

var (
	errBaseDomainRequired = errors.New("base-domain is required, e.g. apps.example.com")
	errAdminTokenRequired = errors.New("admin-token is required; generate one with e.g. openssl rand -hex 24")
)

// ModelOption is one selectable model in the assistant's mode dropdown: an id
// the API understands (or ExternalClaudeMode) and a human label.
type ModelOption struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`
}

// Config is the hostit server configuration, loaded from a YAML file (see LoadConfig)
type Config struct {
	BaseDomain       string  `yaml:"base-domain"`       // Apps live at <app>.<base-domain>; requires wildcard DNS
	AdminToken       string  `yaml:"admin-token"`       // Bearer token for the admin REST API
	ListenHTTP       string  `yaml:"listen-http"`       // HTTP listener (ACME challenges + redirect, or plain proxy if TLS off)
	ListenHTTPS      string  `yaml:"listen-https"`      // HTTPS listener (ignored if TLS off)
	ListenAPI        string  `yaml:"listen-api"`        // Optional extra plain-HTTP admin API listener, e.g. 127.0.0.1:2900
	SocketFile       string  `yaml:"socket-file"`       // Unix socket for the app-side CLI (peercred-authenticated)
	DataDir          string  `yaml:"data-dir"`          // SQLite registry + ACME certs
	AppsDir          string  `yaml:"apps-dir"`          // Home directories of app users
	APIHost          string  `yaml:"api-host"`          // Hostname routed to the admin API; defaults to <base-domain>
	SSHHost          string  `yaml:"ssh-host"`          // Hostname reported for SSH access; defaults to base-domain
	TLS              TLSMode `yaml:"tls"`               // "letsencrypt" or "off"
	LetsEncryptEmail string  `yaml:"letsencrypt-email"` // Optional contact email for ACME

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
	GoogleClientID     string   `yaml:"google-client-id"`     // Google OAuth client ID; empty disables the web login
	GoogleClientSecret string   `yaml:"google-client-secret"` // Google OAuth client secret
	SessionKey         string   `yaml:"session-key"`          // Secret for signing session cookies; generated if empty
	AdminEmails        []string `yaml:"admin-emails"`         // These emails become active admins on first login
	Breakglass         bool     `yaml:"breakglass"`           // Allow the admin token to mint a session for an admin email (no Google); for e2e/recovery
	// BehindProxy serves the full public handler as plain HTTP on ListenHTTP (a
	// local address) instead of terminating TLS: hostit-proxy sits in front and
	// terminates with the cert material this daemon still manages (certmagic +
	// the internal cert endpoint). Session cookies stay Secure -- the browser
	// speaks TLS to the proxy.
	BehindProxy bool `yaml:"behind-proxy"`

	// ListenNode is where control accepts hostit-node dial-ins (mTLS; per-node
	// CN certs from the control-owned CA under data-dir/ipc). When set, this
	// daemon runs CONTROL-ONLY: no machine work happens here until a node
	// connects -- colocated interim: the node shares this host and its store.
	ListenNode string `yaml:"listen-node"`

	// ListenInternal is where the internal surface (routing table, cert
	// material for hostit-proxy; node enrollment later) listens: a unix socket
	// path ("unix:/run/hostit/internal.sock") or a host:port. Empty disables it.
	// Never a public address -- the transport is the auth boundary.
	ListenInternal string `yaml:"listen-internal"`

	// NodeID is this hostit-node's identity: the CN of its mTLS certificate and
	// its name in control's node registry. "local" is the colocated node whose
	// credentials control mints itself under data-dir/ipc; any other name gets
	// its certificate from `hostit-control node add` via the files below.
	NodeID string `yaml:"node-id"`

	// NodeCertFile/NodeKeyFile are this process's cluster identity (the mTLS
	// certificate control and nodes present to each other; CN = node-id, or
	// "control" for control's listener), and ClusterCACertFile is the cluster
	// CA every certificate must chain to. Unset, all three fall back to the
	// auto-minted colocated files under data-dir/ipc, so a single-host split
	// needs no configuration. That is the whole trust setup: possession of a
	// CA-signed certificate is membership; there is no enrollment protocol.
	NodeCertFile      string `yaml:"node-cert-file"`
	NodeKeyFile       string `yaml:"node-key-file"`
	ClusterCACertFile string `yaml:"cluster-ca-cert-file"`

	AppPreview           AppPreviewMode          `yaml:"app-preview"`             // "live" (iframe, default), "screenshot" (periodic headless-chromium shots) or "off"
	AppPreviewIsolation  AppPreviewIsolationMode `yaml:"app-preview-isolation"`   // "strict" (default) or "off"; how the shot container's network is confined
	AppPreviewAllowCIDRs []string                `yaml:"app-preview-allow-cidrs"` // Extra destination CIDRs the shot container may reach in strict mode

	// Built-in coding assistant (the in-browser agent). An empty API key disables it.
	AnthropicAPIKey string `yaml:"anthropic-api-key"` // Anthropic API key for the built-in assistant; empty disables it
	AssistantModel  string `yaml:"assistant-model"`   // Model the assistant uses; defaults to DefaultAssistantModel

	// AssistantModels is the catalog of API models offered in the assistant's
	// mode dropdown (alongside External Claude, when the subscription is set up).
	// Order is the display order; the first is the default API model and the
	// fallback target when External Claude is unavailable.
	AssistantModels []ModelOption `yaml:"assistant-models"`

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
		ListenHTTP:          ":80",
		ListenHTTPS:         ":443",
		SocketFile:          DefaultSocketFile,
		DataDir:             "/var/lib/hostit",
		NodeID:              "local",
		AppsDir:             "/var/lib/hostit/apps",
		TLS:                 TLSLetsEncrypt,
		AppPreview:          AppPreviewLive,
		AppPreviewIsolation: AppPreviewIsolationStrict,
		AssistantModel:      DefaultAssistantModel,
		AssistantModels: []ModelOption{
			{ID: "claude-sonnet-5", Label: "Sonnet 5"},
			{ID: "claude-opus-5", Label: "Opus 5"},
			{ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5"},
		},
	}
}

// ModeOptions returns the full assistant mode catalog for the dropdown: External
// Claude first (only when the subscription is configured), then the API models in
// configured order. The operator's per-user allowlist filters this further.
func (c *Config) ModeOptions() []ModelOption {
	opts := make([]ModelOption, 0, len(c.AssistantModels)+1)
	if c.ClaudeBackendEnabled() {
		opts = append(opts, ModelOption{ID: ExternalClaudeMode, Label: ExternalClaudeLabel})
	}
	opts = append(opts, c.AssistantModels...)
	return opts
}

// IsValidMode reports whether mode is External Claude or one of the configured
// API models (i.e. something a turn may actually run).
func (c *Config) IsValidMode(mode string) bool {
	if mode == ExternalClaudeMode {
		return c.ClaudeBackendEnabled()
	}
	for _, m := range c.AssistantModels {
		if m.ID == mode {
			return true
		}
	}
	return false
}

// DefaultAPIModel is the first configured API model: the default for API turns
// and the target when External Claude falls back. Falls back to AssistantModel.
func (c *Config) DefaultAPIModel() string {
	if len(c.AssistantModels) > 0 {
		return c.AssistantModels[0].ID
	}
	return c.AssistantModel
}

// WebEnabled reports whether Google login (and thus the web app) is configured
func (c *Config) WebEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// RedirectURL is the OAuth callback URL for a login started on the given host.
// Google matches it exactly, so the callback must come back to the hostname the
// user actually visited; every hostname in WebHostnames should be registered.
func (c *Config) RedirectURL(host string) string {
	scheme := "https"
	if c.TLS == TLSOff {
		scheme = "http"
	}
	if !c.IsWebHostname(host) {
		host = c.APIHostname()
	}
	return fmt.Sprintf("%s://%s/auth/callback", scheme, host)
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

// ValidateNode checks what a NODE needs, which is a strict subset: a node
// holds no admin token, base domain, TLS mode or OAuth settings -- those are
// control's, and requiring them would make a legitimate remote-node config
// refuse to start. A colocated node reads the shared server.yml, which has
// everything, so this accepts that too.
func (c *Config) ValidateNode() error {
	if c.NodeID == "" {
		return errors.New("node-id is required")
	}
	if c.AppsDir == "" || c.DataDir == "" || c.SocketFile == "" {
		return errors.New("apps-dir, data-dir and socket-file are required")
	}
	// The cluster credentials are all-or-none: a half-configured triple would
	// otherwise surface later as an opaque TLS failure at dial time.
	set := 0
	for _, f := range []string{c.NodeCertFile, c.NodeKeyFile, c.ClusterCACertFile} {
		if f != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("node-cert-file, node-key-file and cluster-ca-cert-file must be set together")
	}
	return nil
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
