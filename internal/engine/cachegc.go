package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/home-operations/flate/pkg/source"
	"github.com/home-operations/flate/pkg/source/cacheroot"
)

// RunCacheGC prunes flate's on-disk source cache under cacheDir, removing entries
// (fetched Helm charts, OCI layers, git sources) whose mtime is older than ttl.
// Without it the cache only grows — every distinct source any PR ever rendered
// persists on the operator-mounted volume. Bare git mirrors are preserved
// (re-hydrating one is an expensive full clone). It sweeps once immediately and
// then on a cadence, blocking until ctx is cancelled; ttl <= 0 (or an empty
// cacheDir) disables GC and returns immediately.
//
// The sweep is safe to run alongside live renders: flate's source.Sweep holds an
// exclusive GC lock that the cache's writers cooperate with, so a freshly
// referenced blob is never purged mid-fetch.
func RunCacheGC(ctx context.Context, cacheDir string, ttl time.Duration, log *slog.Logger) {
	if ttl <= 0 || cacheDir == "" {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	layout := cacheroot.New(cacheDir)
	sweepCacheGC(ctx, func() {
		res, err := source.Sweep(layout, source.SweepOpts{MaxAge: ttl})
		logSweep(log, res, err)
	}, cacheGCInterval(ttl))
}

// sweepCacheGC is the testable core of RunCacheGC. It runs sweep once immediately
// — so a pod that restarts more often than the interval still reclaims the cache,
// instead of dying before the first tick (a full interval away) ever fires — then
// on every tick until ctx is cancelled.
func sweepCacheGC(ctx context.Context, sweep func(), interval time.Duration) {
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// logSweep records a sweep's outcome. A hard failure, or per-entry errors, are
// surfaced even when nothing was removed — otherwise a chronically failing GC
// (e.g. a permissions error on the cache volume) stays silent while the cache
// grows toward a full disk.
func logSweep(log *slog.Logger, res source.SweepResult, err error) {
	switch {
	case err != nil:
		log.Warn("source cache gc failed", "error", err)
	case len(res.Errors) > 0:
		log.Warn("source cache gc completed with errors",
			"pruned", len(res.Removed), "bytes", res.Bytes, "errors", len(res.Errors))
	case len(res.Removed) > 0:
		log.Info("source cache gc", "pruned", len(res.Removed), "bytes", res.Bytes)
	}
}

// cacheGCInterval picks how often to sweep: several times within a ttl window so
// stale entries are reclaimed promptly, but never more often than hourly nor
// less than every 6h — a long ttl on a long-lived process must not mean the
// sweep effectively never runs.
func cacheGCInterval(ttl time.Duration) time.Duration {
	return min(max(ttl/8, time.Hour), 6*time.Hour)
}
