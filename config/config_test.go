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
	assert.Equal(t, ":443", c.ListenHTTPS)
	assert.Equal(t, "/run/hostit/hostit.sock", c.SocketFile)
	assert.Equal(t, "/var/lib/hostit", c.DataDir)
	assert.Equal(t, "/srv/hostit/apps", c.AppsDir)
	assert.Equal(t, 10000, c.PortMin)
	assert.Equal(t, 19999, c.PortMax)
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
port-min: 20000
port-max: 20010
`), 0600))
	c, err := LoadConfig(filename)
	require.NoError(t, err)
	assert.Equal(t, "apps.example.com", c.BaseDomain)
	assert.Equal(t, "secr3t", c.AdminToken)
	assert.Equal(t, ":8080", c.ListenHTTP)
	assert.Equal(t, TLSOff, c.TLS)
	assert.Equal(t, 20000, c.PortMin)
	assert.Equal(t, 20010, c.PortMax)
	assert.Equal(t, ":443", c.ListenHTTPS) // Default retained
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
		{"bad port range", func(c *Config) { c.PortMin = 5000; c.PortMax = 4000 }, "port"},
		{"bad tls mode", func(c *Config) { c.TLS = "wat" }, "tls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewConfig()
			c.BaseDomain = "apps.example.com"
			c.AdminToken = "secr3t"
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

func TestWildcardTLS(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	c.AdminToken = "secr3t"
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
	c.AdminToken = "secr3t"
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
	// The base domain, plus the historical hostit.<base> so old links survive
	assert.Equal(t, []string{"apps.example.com", "hostit.apps.example.com"}, c.WebHostnames())
	assert.True(t, c.IsWebHostname("apps.example.com"))
	assert.True(t, c.IsWebHostname("hostit.apps.example.com"))
	assert.False(t, c.IsWebHostname("blog.apps.example.com"), "an app subdomain is not the web app")
	assert.False(t, c.IsWebHostname("example.org"))
	// A pinned hostname is included alongside the defaults
	c.APIHost = "admin.example.com"
	assert.Equal(t, []string{"admin.example.com", "apps.example.com", "hostit.apps.example.com"}, c.WebHostnames())
}

func TestSSHHostname(t *testing.T) {
	t.Parallel()
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	assert.Equal(t, "apps.example.com", c.SSHHostname())
	c.SSHHost = "box1.example.com"
	assert.Equal(t, "box1.example.com", c.SSHHostname())
}
