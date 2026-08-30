package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfigDefaults(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	assert.Equal(t, ":80", c.ListenHTTP)
	assert.Equal(t, "/run/hostit/app/hostit.sock", c.SocketFile) // host path; container sees /run/hostit/hostit.sock via the scoped mount
	assert.Equal(t, "/var/lib/hostit", c.DataDir)
	// Under the data directory, not /srv: one place holds hostit's state
	assert.Equal(t, "/var/lib/hostit/apps", c.AppsDir)
	assert.Equal(t, TLSLetsEncrypt, c.TLS)
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "server.yml")
	require.NoError(t, os.WriteFile(filename, []byte(`
base-domain: apps.example.com
admin-token: secr3t
listen-http: ":8080"
tls: "off"
`), 0600))
	c, err := LoadConfig(filename)
	require.NoError(t, err)
	assert.Equal(t, "apps.example.com", c.BaseDomain)
	assert.Equal(t, "secr3t", c.AdminToken)
	assert.Equal(t, ":8080", c.ListenHTTP)
	assert.Equal(t, TLSOff, c.TLS)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "server.yml")
	require.NoError(t, os.WriteFile(filename, []byte("\t: not yaml"), 0600))
	_, err := LoadConfig(filename)
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		modify  func(c *Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"missing base domain", func(c *Config) { c.BaseDomain = "" }, "base-domain"},
		{"missing admin token", func(c *Config) { c.AdminToken = "" }, "admin-token"},
		{"bad tls mode", func(c *Config) { c.TLS = "wat" }, "tls"},
		{"bad app preview mode", func(c *Config) { c.AppPreview = "movie" }, "app-preview"},
		{"screenshot previews", func(c *Config) { c.AppPreview = AppPreviewScreenshot }, ""},
		{"previews off", func(c *Config) { c.AppPreview = AppPreviewOff }, ""},
		{"bad preview isolation", func(c *Config) { c.AppPreviewIsolation = "loose" }, "app-preview-isolation"},
		{"preview isolation off", func(c *Config) { c.AppPreviewIsolation = AppPreviewIsolationOff }, ""},
		{"bad preview allow cidr", func(c *Config) { c.AppPreviewAllowCIDRs = []string{"nope"} }, "app-preview-allow-cidrs"},
		{"good preview allow cidr", func(c *Config) { c.AppPreviewAllowCIDRs = []string{"192.0.2.0/24"} }, ""},
		{"bad outbound cidr", func(c *Config) { c.OutboundAllowPrivateCIDRs = []string{"nope"} }, "outbound-allow-private-cidrs"},
		{"good outbound cidr", func(c *Config) { c.OutboundAllowPrivateCIDRs = []string{"192.168.0.0/16"} }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewConfig()
			c.BaseDomain = "apps.example.com"
			c.AdminToken = "secr3t-secr3t-secr3t"
			tt.modify(c)
			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestAppPreviewDefaultsToLive(t *testing.T) {
	t.Parallel()
	assert.Equal(t, AppPreviewLive, NewConfig().AppPreview)
	assert.Equal(t, AppPreviewIsolationStrict, NewConfig().AppPreviewIsolation)
}

func TestWildcardTLS(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	c.AdminToken = "secr3t-secr3t-secr3t"
	// Without a DNS provider, certificates are issued per app on demand
	assert.False(t, c.WildcardTLS())
	c.DNSProvider = DNSProviderRoute53
	assert.True(t, c.WildcardTLS())
	assert.Equal(t, []string{"*.apps.example.com", "apps.example.com"}, c.CertNames())
	// TLS off disables everything, DNS provider or not
	c.TLS = TLSOff
	assert.False(t, c.WildcardTLS())
}

func TestValidateDNSProvider(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	c.AdminToken = "secr3t-secr3t-secr3t"
	c.DNSProvider = "cloudflare"
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dns-provider")
	c.DNSProvider = DNSProviderRoute53
	require.NoError(t, c.Validate())
}

func TestAPIHostname(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	// The base domain is the web app's home unless the operator pins another
	assert.Equal(t, "apps.example.com", c.APIHostname())
	c.APIHost = "admin.example.com"
	assert.Equal(t, "admin.example.com", c.APIHostname())
}

func TestWebHostnames(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	// The base domain, and only the base domain. A "hostit.<base>" alias used
	// to be here too; two names for one thing is two to register with every
	// OAuth provider and two to write in the documentation.
	assert.Equal(t, []string{"apps.example.com"}, c.WebHostnames())
	assert.True(t, c.IsWebHostname("apps.example.com"))
	assert.False(t, c.IsWebHostname("hostit.apps.example.com"), "the retired alias is an app subdomain now")
	assert.False(t, c.IsWebHostname("blog.apps.example.com"), "an app subdomain is not the web app")
	assert.False(t, c.IsWebHostname("example.org"))
	// A pinned hostname is included alongside the base domain
	c.APIHost = "admin.example.com"
	assert.Equal(t, []string{"admin.example.com", "apps.example.com"}, c.WebHostnames())
}

func TestSSHHostname(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	assert.Equal(t, "apps.example.com", c.SSHHostname())
	c.SSHHost = "box1.example.com"
	assert.Equal(t, "box1.example.com", c.SSHHostname())
}

// The per-component defaults are part of the packaging contract (the .deb ships
// examples there, the units read them, ansible writes them).

func TestClusterSocketDefault(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "/run/hostit/cluster.sock", NewConfig().ClusterSocket)
}

// A short admin token is brute-forceable over HTTPS at line rate; the config
// suggests `openssl rand -hex 24` and this enforces a floor. 16 characters is
// deliberately below any generated token, so only hand-typed weak secrets fail.
func TestValidateRefusesAShortAdminToken(t *testing.T) {
	conf := NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = "hunter2"
	err := conf.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "16")
	conf.AdminToken = "0d772c6a8db50b16565b8cd12d318720"
	assert.NoError(t, conf.Validate())
}

// Per-provider OAuth clients: an instance offers exactly the providers it holds
// a client for, and nothing else.
func TestConnectionClients(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.ConnectionClients = map[string]OAuthClient{
		"slack": {ClientID: "sid", ClientSecret: "ssec"},
	}
	id, secret := c.ConnectionClient("slack")
	assert.Equal(t, "sid", id)
	assert.Equal(t, "ssec", secret)

	id, secret = c.ConnectionClient("discord")
	assert.Empty(t, id)
	assert.Empty(t, secret)
}

// Google's calendar and mail connections fall back to the login client: it is
// the same Google Cloud OAuth client, scopes are requested per authorization
// rather than baked into it, and an instance that can already sign in with
// Google should not need a second registration to read a calendar.
func TestGoogleConnectionsFallBackToTheLoginClient(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.GoogleClientID, c.GoogleClientSecret = "login-id", "login-secret"

	for _, p := range []string{"google-calendar", "gmail"} {
		id, secret := c.ConnectionClient(p)
		assert.Equal(t, "login-id", id, p)
		assert.Equal(t, "login-secret", secret, p)
	}
	// An explicit client still wins, so the two can be separated if wanted
	c.ConnectionClients = map[string]OAuthClient{"gmail": {ClientID: "own", ClientSecret: "own-sec"}}
	id, _ := c.ConnectionClient("gmail")
	assert.Equal(t, "own", id)
	id, _ = c.ConnectionClient("google-calendar")
	assert.Equal(t, "login-id", id, "the other still falls back")

	// And no Google login means no Google connections either
	blank := NewConfig()
	id, _ = blank.ConnectionClient("google-calendar")
	assert.Empty(t, id)
}
