package engine

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestRemoveLegacyCacheLayout covers the upgrade migration: the kustomize stage
// cache of flate <= 0.3.3 and the per-kind layout of flate <= 0.6 are removed
// once at startup, while flate's current `oci` store, its lock, and konflate's
// own `repos` subtree are untouched. A root without any legacy tree is a
// silent no-op.
func TestRemoveLegacyCacheLayout(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	t.Run("removes only the legacy trees", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		legacy := []string{"stage/ab/abcd", "sources/slug/hash", "baselines/rev", "blobs/sha256/ab", "refs/x", "git-mirrors/repo", "helm-tmp/t", "render/helm/ab/abcd"}
		keep := []string{"oci/blobs/sha256", "oci/git/gen", "repos/key/mirror.git", "repos/key/state"}
		for _, p := range slices.Concat(legacy, keep) {
			if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, p, "f"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "oci.lock"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		RemoveLegacyCacheLayout(dir, log)

		for _, name := range legacyCacheDirs {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("legacy tree %s still present (err=%v)", name, err)
			}
		}
		for _, p := range append(keep, "oci.lock") {
			if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
				t.Errorf("current cache entry %s was disturbed: %v", p, err)
			}
		}
	})

	t.Run("no-op without legacy trees", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		RemoveLegacyCacheLayout(dir, log) // must not create or fail anything
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("no-op run materialized entries %v (err=%v)", entries, err)
		}
	})
}
