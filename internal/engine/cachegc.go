package engine

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// legacyCacheDirs are the top-level trees earlier flate versions kept under the
// cache root and the current flate no longer uses: the kustomize stage
// cache of flate <= 0.3.3, and the per-kind layout of flate <= 0.6 (fetched
// sources, baselines, content-addressed blobs and refs, bare git mirrors, Helm
// scratch space, and the persisted Helm render cache). flate >= 0.7 keeps
// everything under one `oci` store, so on a persistent cache volume these
// trees would otherwise sit beside it forever. konflate's own `repos` subtree
// (mirrors and diff state) is not flate's and stays.
var legacyCacheDirs = [...]string{"stage", "sources", "baselines", "blobs", "refs", "git-mirrors", "helm-tmp", "render"}

// RemoveLegacyCacheLayout deletes the legacyCacheDirs present under cacheDir;
// an empty cacheDir is a no-op. flate's current `oci` store is not pruned:
// flate's SDK has no cache sweep, so that takes `flate cache gc` against the
// volume. Size accounting is best-effort — the log line is informational;
// removal proceeds regardless.
func RemoveLegacyCacheLayout(cacheDir string, log *slog.Logger) {
	if cacheDir == "" {
		return
	}
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
