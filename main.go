package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

const summaryInterval = 5 * time.Minute

func main() {
	cfgPath := flag.String("c", "config.json", "path to config file")
	showVersion := flag.Bool("v", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("FireGateway", version)
		return
	}

	start := time.Now()
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		slog.Error("configuration error", "configPath", *cfgPath, "err", err,
			"suggestion", "create config.json based on config.example.json")
		os.Exit(1)
	}
	logCloser, err := setupLogger(cfg.Logging)
	if err != nil {
		slog.Error("failed to set up logging", "err", err)
		os.Exit(1)
	}
	defer logCloser.Close()
	slog.Info("configuration loaded", "version", version, "configPath", *cfgPath, "rules", len(cfg.Forward))

	var proxies []Proxy
	for i := range cfg.Forward {
		r := &cfg.Forward[i]
		switch {
		case r.Status == "active":
			proxies = append(proxies, startRule(r)...)
		case r.ID != "":
			slog.Info("skipping inactive rule", "ruleId", string(r.ID), "name", r.Name, "status", r.Status)
		default:
			slog.Warn("skipping invalid config entry", "index", i, "reason", "missing id")
		}
	}
	slog.Info("proxies started", "count", len(proxies))

	var api *apiServer
	if boolOr(cfg.API.Enabled, true) {
		if api, err = startAPI(cfg.API, proxies, start); err != nil {
			slog.Error("failed to start API server", "err", err)
		}
	}

	ticker := time.NewTicker(summaryInterval)
	defer ticker.Stop()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-ticker.C:
			s := summarize(proxies)
			slog.Info("performance summary", "uptime", formatUptime(int64(time.Since(start).Seconds())),
				"proxies", s.TotalProxies, "totalConnections", s.TotalConnections,
				"activeConnections", s.ActiveConnections, "errors", s.TotalErrors,
				"bytesUpstream", s.BytesUpstream, "bytesDownstream", s.BytesDownstream)
		case s := <-sig:
			slog.Info("shutting down", "signal", s.String())
			if api != nil {
				api.Close()
			}
			for _, p := range proxies {
				p.Close()
			}
			slog.Info("shutdown complete")
			return
		}
	}
}
