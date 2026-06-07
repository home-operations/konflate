package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/home-operations/flate/pkg/source"
	"github.com/home-operations/flate/pkg/source/cacheroot"
)

// RunCacheGC periodically prunes flate's on-disk source cache under cacheDir,
// removing entries (fetched Helm charts, OCI layers, git sources) whose mtime is
// older than ttl. Without it the cache only grows — every distinct source any PR
// ever rendered persists on the operator-mounted volume. Bare git mirrors are
// preserved (re-hydrating one is an expensive full clone). It blocks until ctx
// is cancelled; ttl <= 0 (or an empty cacheDir) disables GC and returns
// immediately.
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
	ticker := time.NewTicker(cacheGCInterval(ttl))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := source.Sweep(layout, source.SweepOpts{MaxAge: ttl})
			switch {
			case err != nil:
				log.Warn("source cache gc failed", "error", err)
			case len(res.Removed) > 0:
				log.Info("source cache gc",
					"pruned", len(res.Removed), "bytes", res.Bytes, "errors", len(res.Errors))
			}
		}
	}
}

// cacheGCInterval picks how often to sweep: several times within a ttl window so
// stale entries are reclaimed promptly, but never more often than hourly nor
// less than every 6h — a long ttl on a long-lived process must not mean the
// sweep effectively never runs.
func cacheGCInterval(ttl time.Duration) time.Duration {
	return min(max(ttl/8, time.Hour), 6*time.Hour)
}
