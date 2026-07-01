package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/home-operations/konflate/internal/api"
)

// imageChecker reports whether a container image reference resolves in its
// registry. Implemented by *registry.Client; an interface so the verify step is
// testable without touching the network.
type imageChecker interface {
	// Exists returns (true,nil) present, (false,nil) definitively absent, and
	// (_,err) indeterminate (auth/network/unparseable). See registry.Client.
	Exists(ctx context.Context, ref string) (bool, error)
}

// imageVerifyConcurrency bounds simultaneous registry dials for one render.
const imageVerifyConcurrency = 8

// verifyImages checks each image a diff newly references — the head-side ref
// (To) of every changed/added ImageChange — against its registry, returning an
// "image-not-found" caution for any that is definitively absent. Indeterminate
// results (auth/network) are skipped, never flagged, so a flaky or private
// registry can't produce a false "missing". Dials run concurrently (bounded)
// with a per-dial timeout; the whole step is also bounded by the render's
// DiffTimeout via ctx. Callers gate this to trusted (non-fork) PRs.
func verifyImages(ctx context.Context, chk imageChecker, images []api.ImageChange, timeout time.Duration, log *slog.Logger) []api.Warning {
	type target struct{ name, ref string }
	var targets []target
	for _, im := range images {
		if im.To == "" { // a removal — nothing new to verify
			continue
		}
		if ref := imageRef(im.Name, im.To); ref != "" {
			targets = append(targets, target{im.Name, ref})
		}
	}
	if len(targets) == 0 {
		return nil
	}

	// One slot per target, written by its own goroutine (no shared-slice race);
	// nil where the image exists or the check was indeterminate.
	results := make([]*api.Warning, len(targets))
	sem := make(chan struct{}, imageVerifyConcurrency)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t target) {
			defer wg.Done()
			defer func() { <-sem }()
			// timeout (cfg.ImageVerifyTimeout) bounds this one registry HEAD; the
			// parent ctx already bounds the whole render (DiffTimeout). timeout<=0
			// (unset) falls back to the render ctx alone.
			cctx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				cctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			ok, err := chk.Exists(cctx, t.ref)
			if err != nil {
				log.Debug("image verify skipped (indeterminate)", "ref", t.ref, "error", err)
				return
			}
			if !ok {
				results[i] = &api.Warning{
					Level:    api.LevelCaution,
					Rule:     "image-not-found",
					Resource: t.name,
					Detail:   fmt.Sprintf("image %s not found in its registry — it would fail to pull", t.ref),
				}
			}
		}(i, t)
	}
	wg.Wait()

	out := make([]api.Warning, 0, len(targets))
	for _, w := range results {
		if w != nil {
			out = append(out, *w)
		}
	}
	return out
}

// imageRef reconstructs a full image reference from an ImageChange's repository
// name and version (a tag, a "tag@sha256:…", or a bare digest).
func imageRef(name, version string) string {
	if name == "" || version == "" {
		return ""
	}
	if strings.HasPrefix(version, "sha256:") {
		return name + "@" + version
	}
	return name + ":" + version
}
