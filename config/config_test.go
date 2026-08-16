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
		{"bad tls mode", func(c *Config) { c.TLS = "wat" }, "tls"},
		{"bad app preview mode", func(c *Config) { c.AppPreview = "movie" }, "app-preview"},
		{"screenshot previews", func(c *Config) { c.AppPreview = AppPreviewScreenshot }, ""},
		{"previews off", func(c *Config) { c.AppPreview = AppPreviewOff }, ""},
		{"bad preview isolation", func(c *Config) { c.AppPreviewIsolation = "loose" }, "app-preview-isolation"},
		{"preview isolation off", func(c *Config) { c.AppPreviewIsolation = AppPreviewIsolationOff }, ""},
		{"bad preview allow cidr", func(c *Config) { c.AppPreviewAllowCIDRs = []string{"nope"} }, "app-preview-allow-cidrs"},
		{"good preview allow cidr", func(c *Config) { c.AppPreviewAllowCIDRs = []string{"192.0.2.0/24"} }, ""},
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

func TestAppPreviewDefaultsToLive(t *testing.T) {
	t.Parallel()
	assert.Equal(t, AppPreviewLive, NewConfig().AppPreview)
	assert.Equal(t, AppPreviewIsolationStrict, NewConfig().AppPreviewIsolation)
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

// A node's config is not control's: it holds no admin token, no base domain,
// no OAuth, no TLS settings -- it dials control and does what it is told. The
// node therefore validates its OWN fields; requiring control's would make a
// legitimate remote-node config refuse to start.
func TestValidateNodeIgnoresControlOnlyFields(t *testing.T) {
	c := NewConfig()
	c.BaseDomain = ""
	c.AdminToken = ""
	c.NodeID = "worker-2"
	c.ListenNode = "10.0.0.1:2930"
	c.NodeCertFile = "/etc/hostit/node.pem"
	c.NodeKeyFile = "/etc/hostit/node.key"
	c.ClusterCACertFile = "/etc/hostit/cluster-ca.pem"

	require.NoError(t, c.ValidateNode())
	require.Error(t, c.Validate(), "control still requires its own fields")
}

// The cluster credential files are all-or-none: a half-configured triple would
// otherwise fail later, at dial time, with a confusing TLS error.
func TestValidateNodeRequiresTheWholeCertTriple(t *testing.T) {
	c := NewConfig()
	c.NodeID = "worker-2"
	c.NodeCertFile = "/etc/hostit/node.pem" // key and CA missing
	require.ErrorContains(t, c.ValidateNode(), "node-key-file")
}

// A colocated node reads the shared server.yml, which has everything; node
// validation must accept that too.
func TestValidateNodeAcceptsAColocatedServerConfig(t *testing.T) {
	c := NewConfig()
	c.BaseDomain = "apps.example.com"
	c.AdminToken = "secr3t"
	require.NoError(t, c.ValidateNode())
}
