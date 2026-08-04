package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/server"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// settingSessionKey stores the generated cookie-signing key, so web sessions
	// survive restarts when the operator did not configure one
	settingSessionKey = "session_key"
)

var (
	cmdServe = &cli.Command{
		Name:   "serve",
		Usage:  "Run the hostit daemon (requires root)",
		Action: execServe,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "server config file"},
		},
	}
)

func execServe(c *cli.Context) error {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return err
	}
	if err := conf.Validate(); err != nil {
		return err
	}
	// 0755: app users must traverse into it for the staged workspace
	// Containerfile; secrets (certs) live in subdirs with their own 0700 perms
	if err := os.MkdirAll(conf.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(conf.DataDir, 0o755); err != nil {
		return err
	}
	s, err := store.NewStore(filepath.Join(conf.DataDir, "hostit.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	ops := app.NewSystemOps()
	manager := app.NewManager(conf, s, ops, app.NewUserRunner(ops))
	users := user.NewManager(conf, s)
	if err := ensureSessionKey(conf, s); err != nil {
		return err
	}
	if err := applyStoredLimits(conf, s, manager, users); err != nil {
		return err
	}
	// Build the shared workspace image once, so app creation and first logins
	// don't pay a per-user image build. This runs in the background: it takes
	// about a minute on a small host, and the proxy must not wait for it (apps
	// created meanwhile fall back to building their own image).
	go func() {
		if err := manager.EnsureSharedImage(); err != nil {
			slog.Warn("Cannot prepare shared workspace image; apps will build their own", "error", err)
		}
	}()
	srv := server.New(conf, manager, users)

	// Periodically measure disk usage and enforce the soft quota
	done := make(chan struct{})
	defer close(done)
	go manager.QuotaLoop(conf.DiskCheckInterval.Duration(), done)

	// Shut down gracefully on SIGINT/SIGTERM
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		srv.Stop()
	}()
	return srv.Run()
}

// ensureSessionKey persists a generated session key, so web logins survive
// restarts even when the operator did not configure one
func ensureSessionKey(conf *config.Config, s *store.Store) error {
	if conf.SessionKey != "" {
		return nil
	}
	settings, err := s.Settings()
	if err != nil {
		return err
	}
	if key, ok := settings[settingSessionKey]; ok && key != "" {
		conf.SessionKey = key
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	conf.SessionKey = hex.EncodeToString(b)
	return s.SetSetting(settingSessionKey, conf.SessionKey)
}

// applyStoredLimits primes the app manager with each app owner's memory and disk
// limits, which live in the user records rather than in the app registry
func applyStoredLimits(conf *config.Config, s *store.Store, apps *app.Manager, users *user.Manager) error {
	registered, err := s.Apps()
	if err != nil {
		return err
	}
	defaults, err := users.Defaults()
	if err != nil {
		return err
	}
	for _, a := range registered {
		limits := defaults
		if a.OwnerID != "" {
			owner, err := users.User(a.OwnerID)
			if err == nil {
				if limits, err = users.Limits(owner); err != nil {
					return err
				}
			}
		}
		apps.SetMemoryLimit(a.Name, limits.MemoryMB)
		apps.SetDiskLimit(a.Name, limits.DiskMB)
	}
	return nil
}
