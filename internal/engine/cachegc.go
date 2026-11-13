package engine

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	sourcecache "github.com/home-operations/flate/pkg/source/cache"
)

// RunCacheGC prunes flate's on-disk source cache under cacheDir, retiring
// entries (fetched Helm charts, OCI layers, git sources) unused for longer than
// ttl and collecting the blobs nothing references any more. Without it the
// cache only grows — every distinct source any PR ever rendered persists on the
// operator-mounted volume. It sweeps once immediately and then on a cadence,
// blocking until ctx is cancelled; ttl <= 0 disables the recurring GC (an empty
// cacheDir disables everything). Independent of ttl, a one-time pass removes the
// cache layouts earlier flate versions left behind — see removeLegacyCacheLayout.
//
// A sweep needs the store's exclusive lock, which a diff in flight holds for
// the duration of its render (see renderCfg). A sweep that finds the store busy
// fails fast with "store is busy" and is retried on the next tick; a render
// that starts mid-sweep waits for the lock instead.
func RunCacheGC(ctx context.Context, cacheDir string, ttl time.Duration, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if cacheDir == "" {
		return
	}
	// One-time migration, independent of the GC ttl: even with GC disabled the
	// retired trees are dead weight nothing else will ever reclaim.
	removeLegacyCacheLayout(cacheDir, log)
	if ttl <= 0 {
		return
	}
	sweepCacheGC(ctx, func() {
		res, err := sourcecache.Sweep(ctx, cacheDir, sourcecache.SweepOpts{MaxAge: ttl})
		logSweep(log, res, err)
	}, cacheGCInterval(ttl))
}

// legacyCacheDirs are the top-level trees earlier flate versions kept under the
// cache root and the current Sweep no longer knows about: the kustomize stage
// cache of flate <= 0.3.3, and the per-kind layout of flate <= 0.6 (fetched
// sources, baselines, content-addressed blobs and refs, bare git mirrors, Helm
// scratch space, and the persisted Helm render cache). flate >= 0.7 keeps
// everything under one `oci` store, so on a persistent cache volume these
// trees would otherwise sit beside it forever. konflate's own `repos` subtree
// (mirrors and diff state) is not flate's and stays.
var legacyCacheDirs = [...]string{"stage", "sources", "baselines", "blobs", "refs", "git-mirrors", "helm-tmp", "render"}

// removeLegacyCacheLayout deletes the legacyCacheDirs present under cacheDir.
// Size accounting is best-effort — the log line is informational; removal
// proceeds regardless.
func removeLegacyCacheLayout(cacheDir string, log *slog.Logger) {
	for _, name := range legacyCacheDirs {
		dir := filepath.Join(cacheDir, name)
		if _, err := os.Stat(dir); err != nil {
			continue // absent (the common case after the first run) or unreadable
		}
		var bytes int64
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // best-effort accounting
			}
			if info, ierr := d.Info(); ierr == nil {
				bytes += info.Size()
			}
			return nil
		})
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("removing legacy cache tree failed", "dir", dir, "error", err)
			continue
		}
		log.Info("removed legacy cache tree left by an earlier flate", "dir", dir, "bytes", bytes)
	}
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
func logSweep(log *slog.Logger, res sourcecache.SweepResult, err error) {
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
