package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lieyanc/FireGateway/internal/api"
	"github.com/lieyanc/FireGateway/internal/auth"
	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/events"
	"github.com/lieyanc/FireGateway/internal/gateway"
	"github.com/lieyanc/FireGateway/internal/logx"
	"github.com/lieyanc/FireGateway/internal/metrics"
	"github.com/lieyanc/FireGateway/internal/updater"
	"github.com/lieyanc/FireGateway/internal/version"
	"github.com/lieyanc/FireGateway/web"
)

const summaryInterval = 5 * time.Minute

func main() {
	cfgPath := flag.String("c", "config.json", "path to config file (created or completed from the default template)")
	showVersion := flag.Bool("v", false, "print version and exit")
	resetAuth := flag.Bool("reset-auth", false, "remove the admin account and API tokens, then exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("FireGateway %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildTime)
		return
	}

	start := time.Now()
	store, state, err := config.Open(*cfgPath)
	if err != nil {
		slog.Error("configuration error", "configPath", *cfgPath, "err", err,
			"suggestion", "fix the file, or remove it to start with defaults")
		os.Exit(1)
	}
	if *resetAuth {
		if _, err := store.Update(func(c *config.Config) error { c.Auth = config.AuthConfig{}; return nil }); err != nil {
			fmt.Fprintln(os.Stderr, "reset failed:", err)
			os.Exit(1)
		}
		fmt.Println("Admin account removed. Restart FireGateway and set it up again in the web UI.")
		return
	}
	cfg := store.Get()
	logCloser, err := logx.Setup(cfg.Logging)
	if err != nil {
		slog.Error("failed to set up logging", "err", err)
		os.Exit(1)
	}
	switch state {
	case config.Created:
		slog.Info("no config file found, created one from the default template", "configPath", *cfgPath)
	case config.Completed:
		slog.Info("config file was missing fields, filled them in with defaults", "configPath", *cfgPath)
	}
	slog.Info("configuration loaded", "version", version.Version, "configPath", *cfgPath, "rules", len(cfg.Forward))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := events.NewBroker()
	mgr := gateway.New(store, broker)
	mgr.Start()
	sampler := metrics.New(mgr, store, broker)
	go sampler.Run(ctx)
	authSvc := auth.New(store)

	var srv *api.Server
	var once sync.Once
	// shutdown releases sockets and flushes state; shared by exit, restart
	// and self-update.
	shutdown := func() {
		once.Do(func() {
			cancel()
			if srv != nil {
				srv.Close()
			}
			mgr.Stop()
			if err := sampler.Save(); err != nil {
				slog.Warn("failed to save metrics history", "err", err)
			}
			slog.Info("shutdown complete")
			logCloser.Close()
		})
	}
	beforeExec := func() error { shutdown(); return nil }

	upd := updater.New(func() updater.Config {
		u := store.Get().Update
		return updater.Config{Enabled: u.Enabled, Channel: u.Channel, CheckInterval: u.CheckInterval,
			Source: u.Source, ProxyBaseURL: u.ProxyBaseURL, Repo: u.Repo}
	}, func() string { return store.Get().DataDir }, slog.Default(), updater.RestartHooks{
		BeforeExec: func(string) error { return beforeExec() },
		// Resources are already released; let the supervisor restart us.
		OnExecFailure: func(err error) { slog.Error("restart after update failed", "err", err); os.Exit(1) },
		// Give live connections a chance to finish before an update restart.
		IsBusy: func() bool {
			for _, rn := range mgr.Runners() {
				if rn.Snapshot().Active > 0 {
					return true
				}
			}
			return false
		},
	})
	upd.StartBackground(ctx)

	if cfg.API.Enabled {
		srv, err = api.Start(api.Deps{
			Store: store, Manager: mgr, Sampler: sampler, Broker: broker, Auth: authSvc, Updater: upd,
			Started: start, Web: web.FS(),
			Restart: func() error {
				err := updater.Restart(beforeExec)
				// Only reached if exec failed after resources were released.
				slog.Error("restart failed", "err", err)
				os.Exit(1)
				return err
			},
		})
		if err != nil {
			slog.Error("failed to start web UI / API server", "err", err)
		} else if tok := authSvc.SetupToken(); tok != "" {
			url := "http://" + net.JoinHostPort(cfg.API.Host, strconv.Itoa(cfg.API.Port))
			slog.Warn("admin account not set up yet: open the web UI and enter this setup token", "url", url, "setupToken", tok)
		}
	}

	ticker := time.NewTicker(summaryInterval)
	defer ticker.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		select {
		case <-ticker.C:
			var t metrics.Counters
			for _, rn := range mgr.Runners() {
				t.Add(metrics.CountersOf(rn.Snapshot(), 0, 0))
			}
			slog.Info("performance summary", "uptime", api.FormatUptime(int64(time.Since(start).Seconds())),
				"rules", len(mgr.Runners()), "totalConnections", t.TotalConnections,
				"activeConnections", t.ActiveConnections, "errors", t.Errors, "rejected", t.Rejected,
				"bytesUpstream", t.BytesUp, "bytesDownstream", t.BytesDown)
		case s := <-sig:
			if s == syscall.SIGHUP {
				if _, c, err := mgr.Reload(); err != nil {
					slog.Error("reload failed", "err", err)
				} else if l, ok := logx.ParseLevel(c.Logging.Level); ok {
					logx.Level.Set(l)
				}
				continue
			}
			slog.Info("shutting down", "signal", s.String())
			shutdown()
			return
		}
	}
}
