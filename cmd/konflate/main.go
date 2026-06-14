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

	// Blank import: its init() sets GOMEMLIMIT to 90% of the container's cgroup
	// memory limit (honoring an explicit GOMEMLIMIT / AUTOMEMLIMIT=off). The GC is
	// otherwise unaware of the cgroup limit — unlike GOMAXPROCS, cgroup-aware
	// since Go 1.25 — so a memory-heavy render could OOM-kill the pod first.
	_ "github.com/KimMachineGun/automemlimit"
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

	logger.Info("starting konflate",
		"version", version,
		"commit", commit,
		"repo", cfg.Repo,
		"forge", cfg.Forge.Kind,
		"authenticated", cfg.Authenticated(),
		"checks", cfg.ChecksEnabled(),
		"port", cfg.Port,
		"metrics_addr", cfg.MetricsAddr,
		"diff_concurrency", cfg.MaxDiffConcurrency,
		"refresh_interval", cfg.RefreshInterval,
		"webhook", cfg.WebhookEnabled(),
		"push", cfg.PushEnabled(),
		"mcp", cfg.MCPEnabled(),
	)

	prov, err := provider.New(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	// The renderer clones with the same forge identity as the API client — a PAT or
	// a GitHub App installation token — so it reaches private repos and isn't capped
	// by the anonymous rate limit.
	gitToken, err := provider.GitTokenSource(cfg)
	if err != nil {
		return fmt.Errorf("git credential: %w", err)
	}

	srv := server.New(cfg, prov, engine.New(cfg, gitToken), web.FS(), logger)
	srv.Version = version // surfaced at /api/meta for the UI footer

	// Graceful shutdown on the usual termination signals. SIGQUIT is deliberately
	// left to Go's default (goroutine dump + crash) so a wedged drain stays
	// diagnosable, and SIGHUP is not captured. stop() runs as soon as the first
	// signal arrives — not just deferred to run's end — so a second signal restores
	// default handling and force-terminates instead of being swallowed during a
	// slow drain.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // re-arm default termination for a second signal
	}()

	// Best-effort background GC of flate's on-disk source cache so it doesn't
	// grow without bound across the process lifetime. Stops with ctx; disabled
	// when CacheTTL <= 0.
	go engine.RunCacheGC(ctx, cfg.CacheDir, cfg.CacheTTL, logger)

	return srv.Run(ctx)
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
