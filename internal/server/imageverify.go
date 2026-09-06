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
// (To) of every changed/added ImageChange — against its registry. Each checked
// image gets its Upstream stamped in place (ImageFound / ImageMissing), and a
// definitively absent one yields an "image-not-found" blocker per referencing
// resource (Refs; the image name when there are none), so the finding lands on
// the workload that would ImagePullBackOff and fails the check. Indeterminate
// results (auth/network) leave Upstream empty and are never flagged, so a flaky
// or private registry can't produce a false "missing". Dials run concurrently
// (bounded) with a per-dial timeout; the whole step is also bounded by the
// render's DiffTimeout via ctx. Callers gate this to trusted (non-fork) PRs.
func verifyImages(ctx context.Context, chk imageChecker, images []api.ImageChange, timeout time.Duration, log *slog.Logger) []api.Warning {
	type target struct {
		idx     int // index into images, to stamp Upstream
		name    string
		version string // To, as recorded on the change
		ref     string // the pullable reference dialed
		refs    []string
	}
	var targets []target
	for i, im := range images {
		if im.To == "" { // a removal — nothing new to verify
			continue
		}
		if ref := imageRef(im.Name, im.To); ref != "" {
			targets = append(targets, target{i, im.Name, im.To, ref, im.Refs})
		}
	}
	if len(targets) == 0 {
		return nil
	}

	// One slot per target, written by its own goroutine (no shared-slice race);
	// nil where the image exists or the check was indeterminate. images[t.idx]
	// is likewise owned by exactly one goroutine.
	results := make([][]api.Warning, len(targets))
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
			if ok {
				images[t.idx].Upstream = api.ImageFound
				return
			}
			images[t.idx].Upstream = api.ImageMissing
			results[i] = imageNotFound(t.name, t.version, t.refs)
		}(i, t)
	}
	wg.Wait()

	var out []api.Warning
	for _, ws := range results {
		out = append(out, ws...)
	}
	return out
}

// imageNotFound builds the blockers for one absent image: one per referencing
// resource so each lands on (and deep-links to) the workload that would fail to
// pull, or a single one on the image name when the diff recorded no referrers.
// The detail names the image by tag (a digest-pinned ref drops the digest; a
// bare digest is shortened): the finding is read, not pasted into a pull.
func imageNotFound(name, version string, refs []string) []api.Warning {
	resources := refs
	if len(resources) == 0 {
		resources = []string{name}
	}
	shown := imageRef(name, tagOf(version))
	if tagOf(version) == version { // no tag to fall back on: a bare digest
		shown = imageRef(name, shortVer(version))
	}
	out := make([]api.Warning, 0, len(resources))
	for _, r := range resources {
		out = append(out, api.Warning{
			Level:    api.LevelBlocking,
			Rule:     "image-not-found",
			Resource: r,
			Detail:   fmt.Sprintf("image %s not found in upstream registry", shown),
		})
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
