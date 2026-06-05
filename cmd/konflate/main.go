// Command konflate serves a web UI for reviewing GitHub/GitLab/Forgejo pull
// requests as rendered Flux diffs.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/engine"
	"github.com/home-operations/konflate/internal/provider"
	"github.com/home-operations/konflate/internal/server"
	"github.com/home-operations/konflate/internal/web"
)

// Build metadata, set via -ldflags at release time (see Dockerfile / release.yaml).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("konflate exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	setMemoryLimit(logger)

	logger.Info("starting konflate",
		"version", version,
		"commit", commit,
		"repo", cfg.Repo,
		"forge", cfg.Forge.Kind,
		"authenticated", cfg.Authenticated(),
		"port", cfg.Port,
		"metrics_addr", cfg.MetricsAddr,
		"diff_concurrency", cfg.MaxDiffConcurrency,
		"refresh_interval", cfg.RefreshInterval,
		"webhook", cfg.WebhookEnabled(),
		"push", cfg.PushEnabled(),
	)

	prov, err := provider.New(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	srv := server.New(cfg, prov, engine.New(cfg), web.FS(), logger)

	// Honour the usual termination signals plus SIGHUP/SIGQUIT (matching the
	// org's services) for a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()

	return srv.Run(ctx)
}

// memLimitRatio is the fraction of the container memory limit GOMEMLIMIT is set
// to, leaving headroom for goroutine stacks and runtime overhead so the GC
// tightens before the kernel OOM-kills the process.
const memLimitRatio = 0.9

// setMemoryLimit aligns Go's soft memory limit (GOMEMLIMIT) with the container's
// cgroup memory limit. Unlike GOMAXPROCS — cgroup-aware since Go 1.25 — the GC
// never learns the cgroup limit on its own, so a hard memory limit can OOM-kill
// a memory-heavy process (konflate holds every rendered diff in memory) before
// the GC reacts. automemlimit reads the cgroup (v1 or v2) limit and sets
// GOMEMLIMIT to memLimitRatio of it. An explicit GOMEMLIMIT (or AUTOMEMLIMIT=off)
// always wins; a missing limit or non-Linux host leaves the GC unbounded.
func setMemoryLimit(log *slog.Logger) {
	limit, err := memlimit.SetGoMemLimitWithOpts(memlimit.WithRatio(memLimitRatio))
	switch {
	case err != nil:
		log.Debug("no cgroup memory limit detected; leaving the GC unbounded", "error", err)
	case limit > 0:
		log.Info("set GOMEMLIMIT from cgroup memory limit", "gomemlimit_bytes", limit, "ratio", memLimitRatio)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
