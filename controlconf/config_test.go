package controlconf

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
func TestResolveConfigFilePrefersTheComponentFile(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "control.yml")
	legacy := filepath.Join(dir, "server.yml")
	require.NoError(t, os.WriteFile(own, []byte("base-domain: a.example.com\n"), 0o600))
	require.NoError(t, os.WriteFile(legacy, []byte("base-domain: legacy.example.com\n"), 0o600))

	assert.Equal(t, own, ResolveConfigFile(own, legacy), "the component's own file wins")
}

func TestResolveConfigFileFallsBackToTheLegacySharedFile(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "node.yml") // never created
	legacy := filepath.Join(dir, "server.yml")
	require.NoError(t, os.WriteFile(legacy, []byte("node-id: local\n"), 0o600))

	assert.Equal(t, legacy, ResolveConfigFile(own, legacy), "a pre-split install keeps running")
}

func TestResolveConfigFileKeepsTheIntendedPathWhenNeitherExists(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "control.yml")
	// Neither file exists: report the path the operator is meant to create, so
	// the error names the new location rather than the retired one.
	assert.Equal(t, own, ResolveConfigFile(own, filepath.Join(dir, "server.yml")))
}

// The per-component defaults are part of the packaging contract (the .deb ships
// examples there, the units read them, ansible writes them).

// listen-node became listen-cluster when proxies started dialing the same
// listener; an existing config must keep working, the way a node's retired
// listen-node key does.
func TestListenClusterHonoursTheRetiredListenNodeKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "control.yml")
	require.NoError(t, os.WriteFile(path, []byte("base-domain: apps.example.com\nadmin-token: t\nlisten-node: 10.0.0.1:2930\n"), 0o600))

	c, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:2930", c.ListenCluster, "the old key still names the remote listener")

	// The new name wins when both are present.
	require.NoError(t, os.WriteFile(path, []byte("listen-node: 10.0.0.1:2930\nlisten-cluster: 10.0.0.2:2930\n"), 0o600))
	c, err = LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2:2930", c.ListenCluster)

	// And the same-host socket has a name of its own.
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
