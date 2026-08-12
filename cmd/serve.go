package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/run"
	"heckel.io/hostit/server"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// settingSessionKey stores the generated cookie-signing key, so web sessions
	// survive restarts when the operator did not configure one
	settingSessionKey = "session_key"
	// dataDirMode lets app users traverse /var/lib/hostit to their own home
	// without reading it; the registry inside is 0600
	dataDirMode = 0o711
	// appsDirMode is the directory holding the app homes, each 0750 in turn
	appsDirMode = 0o755
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
	// Refuse to start on a host that cannot support the daemon (not root, a missing
	// command), rather than failing lazily on the first app operation.
	if err := checkHostRequirements(); err != nil {
		return err
	}
	// 0711: app users must traverse this to reach their own home below it, but
	// must not be able to list it. What lives here is the registry -- every app's
	// agent token and the session signing key -- which is 0600 besides.
	if err := os.MkdirAll(conf.DataDir, dataDirMode); err != nil {
		return err
	}
	if err := os.Chmod(conf.DataDir, dataDirMode); err != nil {
		return err
	}
	if err := os.MkdirAll(conf.AppsDir, appsDirMode); err != nil {
		return err
	}
	if err := os.Chmod(conf.AppsDir, appsDirMode); err != nil {
		return err
	}
	// btrfs is mandatory: snapshots, rollback, fork and hard disk quotas are core.
	// Check it here, once the app-homes directory exists, and refuse to start
	// otherwise rather than silently running without those features.
	if err := requireBtrfs(conf.AppsDir); err != nil {
		return err
	}
	s, err := store.NewStore(filepath.Join(conf.DataDir, "hostit.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	app.Version = c.App.Version // Part of each container's identity; see app.Version
	manager := app.NewManager(conf, s, app.NewSystemServices(run.New()))
	users := user.NewManager(conf, s)
	if err := ensureSessionKey(conf, s); err != nil {
		return err
	}
	if err := applyStoredLimits(s, manager, users); err != nil {
		return err
	}
	// Build the workspace image once. This runs in the background: it takes
	// about a minute on a small host, and the proxy must not wait for it.
	go func() {
		if err := manager.EnsureWorkspaceImage(); err != nil {
			slog.Warn("Cannot prepare shared workspace image; apps will build their own", "error", err)
		}
		// Agents keep the behaviour of the binary they were exec'd from, so an
		// upgrade only reaches them on a restart. In the background: this costs
		// each app a moment, and the proxy should be up first.
		restarted, err := manager.RestartStaleAgents(c.App.Version)
		if err != nil {
			slog.Warn("Cannot restart apps after upgrade", "error", err)
		} else if len(restarted) > 0 {
			slog.Info("Restarted apps to pick up the new version", "apps", restarted, "version", c.App.Version)
		}
		// Once the apps are on the current image, its predecessors are dead weight
		manager.PruneOldWorkspaceImages()
	}()
	srv := server.New(conf, manager, users)

	// The registry is the source of truth: an app deleted while the daemon was
	// down, or whose unit outlived it, leaves systemd retrying something that no
	// longer exists
	if removed := manager.ReconcileOrphans(); len(removed) > 0 {
		slog.Info("Cleaned up leftovers of apps that no longer exist", "apps", removed)
	}

	// Periodically measure disk usage for the dashboard (btrfs qgroups do the
	// actual enforcement at write time)
	done := make(chan struct{})
	defer close(done)
	go manager.DiskUsageLoop(done)
	go manager.StateLoop(done)
	// Hourly automatic snapshots (a no-op unless the apps filesystem is btrfs)
	go manager.SnapshotLoop(time.Hour, done)
	// Retry pending/error custom domains so they verify once DNS is set up
	go srv.DomainRetryLoop(time.Minute, done)

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
func applyStoredLimits(s *store.Store, apps *app.Manager, users *user.Manager) error {
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
