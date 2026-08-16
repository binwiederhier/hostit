package proxyd

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

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigFile is where the proxy's config lives on a package install
	DefaultConfigFile = "/etc/hostit/proxy.yml"
	readHeaderTimeout = 10 * time.Second
)

// FileConfig is /etc/hostit/proxy.yml: where control is and where to listen.
type FileConfig struct {
	ControlURL  string `yaml:"control-url"`  // control's local HTTP listener (traffic fallback)
	InternalURL string `yaml:"internal-url"` // control's internal listener (routes + certs); defaults to control-url
	ListenHTTPS string `yaml:"listen-https"` // default :443
	ListenHTTP  string `yaml:"listen-http"`  // default :80
	CacheDir    string `yaml:"cache-dir"`    // routes + cert cache; default /var/lib/hostit-proxy
}

// LoadFileConfig reads the YAML config on top of defaults.
func LoadFileConfig(path string) (*FileConfig, error) {
	conf := &FileConfig{ListenHTTPS: ":443", ListenHTTP: ":80", CacheDir: "/var/lib/hostit-proxy"}
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
	return conf, nil
}

// NewCLI is the hostit-proxy command line: one job, `serve`.
func NewCLI() *cli.App {
	return &cli.App{
		Name:  "hostit-proxy",
		Usage: "hostit's data plane: terminates TLS and routes to apps from a cached table",
		Commands: []*cli.Command{{
			Name:   "serve",
			Usage:  "Run the proxy",
			Action: execServe,
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: DefaultConfigFile, Usage: "proxy config file"},
			},
		}},
	}
}

func execServe(c *cli.Context) error {
	conf, err := LoadFileConfig(c.String("config"))
	if err != nil {
		return err
	}
	p := New(&Config{ControlURL: conf.ControlURL, InternalURL: conf.InternalURL, CacheDir: conf.CacheDir})
	done := make(chan struct{})
	defer close(done)
	go p.WatchRoutes(done)

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
