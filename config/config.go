// Package config defines the hostit server configuration and its YAML loading logic.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
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
	// DefaultServerConfigFile is where "hostit serve" looks for its config by default
	DefaultServerConfigFile = "/etc/hostit/server.yml"
	// DNSProviderRoute53 enables DNS-01 challenges via AWS Route 53, which is
	// what a wildcard certificate requires (Let's Encrypt does not issue
	// wildcards over HTTP-01)
	DNSProviderRoute53 = "route53"
)

var (
	errBaseDomainRequired = errors.New("base-domain is required, e.g. apps.example.com")
	errAdminTokenRequired = errors.New("admin-token is required; generate one with e.g. openssl rand -hex 24")
)

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
	PortMin         int    `yaml:"port-min"` // Lower bound of the per-app loopback port range
	PortMax         int    `yaml:"port-max"` // Upper bound of the per-app loopback port range

	// Web app and user accounts
	GoogleClientID     string   `yaml:"google-client-id"`     // Google OAuth client ID; empty disables the web login
	GoogleClientSecret string   `yaml:"google-client-secret"` // Google OAuth client secret
	SessionKey         string   `yaml:"session-key"`          // Secret for signing session cookies; generated if empty
	AdminEmails        []string `yaml:"admin-emails"`         // These emails become active admins on first login
	DiskCheckInterval  Duration `yaml:"disk-check-interval"`  // How often app disk usage is measured
}

// NewConfig returns a Config with all defaults set; BaseDomain and AdminToken must be filled in
func NewConfig() *Config {
	return &Config{
		ListenHTTP:        ":80",
		ListenHTTPS:       ":443",
		SocketFile:        "/run/hostit/hostit.sock",
		DataDir:           "/var/lib/hostit",
		AppsDir:           "/var/lib/hostit/apps",
		TLS:               TLSLetsEncrypt,
		PortMin:           10000,
		PortMax:           19999,
		DiskCheckInterval: Duration(15 * time.Minute),
	}
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
func (c *Config) Validate() error {
	if c.BaseDomain == "" {
		return errBaseDomainRequired
	}
	if c.AdminToken == "" {
		return errAdminTokenRequired
	}
	if c.PortMin <= 0 || c.PortMax < c.PortMin {
		return fmt.Errorf("invalid port range %d-%d", c.PortMin, c.PortMax)
	}
	if c.TLS != TLSLetsEncrypt && c.TLS != TLSOff {
		return fmt.Errorf("invalid tls mode %q, must be %q or %q", c.TLS, TLSLetsEncrypt, TLSOff)
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
