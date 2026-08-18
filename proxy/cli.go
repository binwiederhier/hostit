package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigFile is where the proxy's config lives on a package install;
	// like every component, it owns a directory under /etc/hostit.
	// legacyConfigFile is the pre-split path, still honored (see Serve).
	DefaultConfigFile = "/etc/hostit/proxy/proxy.yml"
	legacyConfigFile  = "/etc/hostit/proxy.yml"
	readHeaderTimeout = 10 * time.Second
	// DefaultProxyID is the colocated proxy, which exists implicitly the way
	// the colocated node does.
	DefaultProxyID = "local"
	// The colocated proxy's credentials: the pair control mints for it under
	// its own data dir, so a single-box install enrolls nothing.
	DefaultLocalCertFile   = "/var/lib/hostit/ipc/proxy-local.pem"
	DefaultLocalKeyFile    = "/var/lib/hostit/ipc/proxy-local.key"
	DefaultLocalCACertFile = "/var/lib/hostit/ipc/ca.pem"
	// defaultClusterURL is control's same-host member socket, which is where a
	// colocated proxy dials unless told otherwise. No certificate is involved.
	defaultClusterURL = "unix:/run/hostit/cluster.sock"
)

// FileConfig is /etc/hostit/proxy/proxy.yml: who this proxy is, where control
// is, and where to listen.
type FileConfig struct {
	// ProxyID is this proxy's cluster identity, and must match the CN of its
	// certificate. "local" is the colocated proxy.
	ProxyID string `yaml:"proxy-id"`
	// ControlURL is control's local HTTP listener: the dashboard/API upstream
	// and the unknown-host fallback. ClusterURL is where this proxy dials in
	// for its configuration, as host:port; it defaults to control-url's host
	// with the cluster port, which is right when they share a machine.
	ControlURL string `yaml:"control-url"`
	ClusterURL string `yaml:"cluster-url"`
	// The cluster credentials, minted by `hostit-control proxy add`. On a
	// colocated proxy they are the pair control keeps under its data dir.
	CertFile    string `yaml:"proxy-cert-file"`
	KeyFile     string `yaml:"proxy-key-file"`
	CACertFile  string `yaml:"cluster-ca-cert-file"`
	ListenHTTPS string `yaml:"listen-https"` // default :443
	ListenHTTP  string `yaml:"listen-http"`  // default :80
	CacheDir    string `yaml:"cache-dir"`    // routes + cert cache; default /var/lib/hostit-proxy
}

// LoadFileConfig reads the YAML config on top of defaults.
func LoadFileConfig(path string) (*FileConfig, error) {
	conf := &FileConfig{ProxyID: DefaultProxyID, ListenHTTPS: ":443", ListenHTTP: ":80", CacheDir: "/var/lib/hostit-proxy",
		CertFile:   DefaultLocalCertFile,
		KeyFile:    DefaultLocalKeyFile,
		CACertFile: DefaultLocalCACertFile}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, conf); err != nil {
		return nil, fmt.Errorf("cannot parse config %s: %w", path, err)
	}
	if conf.ControlURL == "" {
		return nil, fmt.Errorf("control-url is required")
	}
	if conf.ClusterURL == "" {
		conf.ClusterURL = defaultClusterURL
	}
	return conf, nil
}

// Serve runs the proxy from its config file until a termination signal.
func Serve(configPath string) error {
	// Pre-split installs keep their /etc/hostit/proxy.yml until it is moved.
	if _, err := os.Stat(configPath); err != nil {
		if _, legacyErr := os.Stat(legacyConfigFile); legacyErr == nil {
			slog.Warn("Reading the pre-split proxy config; move it to the component's own file", "file", legacyConfigFile, "expected", configPath)
			configPath = legacyConfigFile
		}
	}
	conf, err := LoadFileConfig(configPath)
	if err != nil {
		return err
	}
	p := New(&Config{
		ProxyID:    conf.ProxyID,
		ControlURL: conf.ControlURL,
		ClusterURL: conf.ClusterURL,
		CertFile:   conf.CertFile,
		KeyFile:    conf.KeyFile,
		CACertFile: conf.CACertFile,
		CacheDir:   conf.CacheDir,
	})
	done := make(chan struct{})
	defer close(done)
	go p.Link(done)

	// :80 -- ACME HTTP-01 challenges pass through to control (which answers
	// them), everything else redirects to HTTPS. The proxy owns the redirect
	// now; control behind it only ever sees forwarded traffic.
	httpServer := &http.Server{Addr: conf.ListenHTTP, ReadHeaderTimeout: readHeaderTimeout,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
				p.control.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "https://"+hostOnly(r.Host)+r.URL.RequestURI(), http.StatusMovedPermanently)
		})}
	go func() {
		slog.Info("Listening for HTTP (ACME pass-through + redirect)", "addr", conf.ListenHTTP)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP listener failed", "error", err)
		}
	}()

	// :443 -- terminate with control-managed material, route from the cache.
	httpsServer := &http.Server{
		Addr:              conf.ListenHTTPS,
		Handler:           p,
		ReadHeaderTimeout: readHeaderTimeout,
		TLSConfig: &tls.Config{
			GetCertificate: p.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
			MinVersion:     tls.VersionTLS12,
		},
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		_ = httpsServer.Close()
		_ = httpServer.Close()
	}()
	slog.Info("Listening for HTTPS", "addr", conf.ListenHTTPS, "control", conf.ControlURL)
	err = httpsServer.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
