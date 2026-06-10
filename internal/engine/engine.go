// Package engine turns a pull request into a rendered diff. The flate-backed
// implementation clones the repo, renders the Flux cluster at both the
// merge-base and the head with flate, pairs the two render outputs into a set
// of resource-level changes, and hands them to the diff renderer.
//
// The server depends on the [Engine] interface (not the concrete type) so it
// can be exercised with a fake; the pairing and image-extraction logic is pure
// and unit-tested directly, while the clone+render path is integration-tested.
package engine

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	flatediff "github.com/home-operations/flate/pkg/diff"
	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/orchestrator"
	"github.com/home-operations/flate/pkg/source"
	"github.com/home-operations/flate/pkg/source/cacheroot"
	"github.com/home-operations/flate/pkg/store"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/diff"
	"github.com/home-operations/konflate/internal/gitclone"
)

// Engine renders a pull request into a structured diff.
type Engine interface {
	Diff(ctx context.Context, pr api.PR) (api.DiffResult, error)
}

// flateEngine is the production Engine: a persistent git mirror + two flate
// renders.
type flateEngine struct {
	clusterPath string
	// selfURLs is the repo's own remote URL(s). flate aliases a
	// self-referential GitRepository (a cluster that pulls itself) to the
	// extracted tree only when its spec.url matches one of these — the
	// trees carry no .git for flate to read remotes from.
	selfURLs []string
	cacheDir string
	// mirror is the persistent bare clone of the repo; each diff fetches the
	// base/head refs into it incrementally instead of re-cloning.
	mirror *gitclone.Mirror
	// pullHeadRef maps a PR number to the forge's server-side pull head ref
	// (refs/pull/N/head; refs/merge-requests/N/head on GitLab). The mirror fetches
	// the head from this ref rather than the head branch, so cross-repo (fork) PRs
	// — whose branch lives in the contributor's repo — still resolve.
	pullHeadRef func(number int) string
	// cache is shared across every diff for this repo instance and, within a
	// single diff, across the base and head renders — so the head render reuses
	// the Helm charts / OCI layers / git sources the base render already
	// fetched. flate's source cache is concurrency-safe (audited).
	cache *source.Cache

	// flate render tuning, resolved from config once (see internal/config). The
	// helm/stage caches and source retry default to flate's own CLI values; the
	// reconcile concurrency is bounded so a fan-out PR can't oversubscribe.
	stageCacheBytes        int64
	helmTemplateCacheBytes int64
	helmRenderCacheBytes   int64
	concurrency            int
	sourceRetry            source.RetryConfig
	diffTimeout            time.Duration
	maxDiffResources       int
}

// New builds the production Engine from config.
func New(cfg *config.Config) Engine {
	const mib = 1 << 20
	conc := cfg.RenderConcurrency
	if conc <= 0 {
		conc = runtime.NumCPU() * 4 // flate's default for I/O-bound reconcile work
	}
	return &flateEngine{
		clusterPath:            cfg.ClusterPath,
		selfURLs:               []string{cfg.Forge.CloneURL()},
		cacheDir:               cfg.CacheDir,
		mirror:                 gitclone.NewMirror(cfg.CacheDir, cfg.CloneDir, cfg.Forge.CloneURL(), cfg.Token, cfg.FetchTimeout),
		pullHeadRef:            cfg.Forge.PullHeadRef,
		cache:                  source.NewCache(cacheroot.New(cfg.CacheDir)),
		stageCacheBytes:        int64(cfg.StageCacheMB) * mib,
		helmTemplateCacheBytes: int64(cfg.HelmTemplateCacheMB) * mib,
		helmRenderCacheBytes:   int64(cfg.HelmRenderCacheMB) * mib,
		concurrency:            conc,
		sourceRetry: source.RetryConfig{
			Attempts: cfg.SourceRetryAttempts,
			MinWait:  200 * time.Millisecond,
			MaxWait:  3 * time.Second,
			Jitter:   0.1,
		},
		diffTimeout:      cfg.DiffTimeout,
		maxDiffResources: cfg.MaxDiffResources,
	}
}

// Diff clones the PR, renders both sides with flate, and builds the diff. The
// whole operation is bounded by diffTimeout (when set) so one PR can't hold a
// render slot indefinitely — the deadline flows into the git fetch and both
// flate renders via ctx.
func (e *flateEngine) Diff(ctx context.Context, pr api.PR) (api.DiffResult, error) {
	if e.diffTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.diffTimeout)
		defer cancel()
	}

	clone, err := e.mirror.Trees(ctx, e.pullHeadRef(pr.Number), pr.BaseRef)
	if err != nil {
		return api.DiffResult{}, fmt.Errorf("engine: clone PR #%d: %w", pr.Number, err)
	}
	defer clone.Cleanup()

	// flate's RenderTrees owns the two-orchestrator dance: each side
	// reconciles changed-only against the other (only resources whose source
	// files differ between the merge-base and head trees, plus the dependency
	// closure flate computes — not the whole cluster, the dominant cold-start
	// cost saver), sharing e.cache so the head render reuses the base render's
	// fetched charts / OCI layers / git sources, concurrently.
	//
	// Each side is a Tree of its extracted repo root (RepoRoot — the anchor
	// repo-root-relative spec.path values resolve against) and the cluster's
	// Flux entry point within it (Path = root + clusterPath). Passing the root
	// explicitly is what makes clusterPath work: the trees have no .git, so
	// flate can't infer the root, and a repo-root-relative spec.path
	// (./kubernetes/...) would otherwise double under the entry-point subdir.
	// selfURLs lets a self-referential GitRepository alias to the tree.
	//
	// Per flate's contract the returned err is advisory: a side's Result is nil
	// only on a fatal Bootstrap error, whereas per-resource reconcile failures
	// keep Result non-nil and ARE the diff's render failures — they must flow to
	// renderFailures below, not abort the whole diff (a PR that breaks a chart is
	// exactly the one a reviewer needs to see). renderUsable gates on that.
	base, head, err := orchestrator.RenderTrees(ctx,
		orchestrator.Tree{RepoRoot: clone.BaseDir, Path: joinPath(clone.BaseDir, e.clusterPath), SelfURLs: e.selfURLs},
		orchestrator.Tree{RepoRoot: clone.HeadDir, Path: joinPath(clone.HeadDir, e.clusterPath), SelfURLs: e.selfURLs},
		e.renderCfg())
	if !renderUsable(base, head, err) {
		return api.DiffResult{}, fmt.Errorf("engine: render PR #%d: %w", pr.Number, err)
	}

	// A parent (HelmRelease/Kustomization) whose render failed on a side produced
	// no manifests there, so pairing it against the other side reads as a wholesale
	// add/remove of all its resources — a transient OCI/source timeout looking like
	// "deleted every CRD" (with false data-loss cautions). Surface the failure, but
	// drop those phantom changes (and the cautions/image diffs derived from them) so
	// a timeout can't masquerade as a deletion.
	failures := renderFailures(base.Result.Failed, head.Result.Failed)
	changes := dropFailedParents(pairChanges(base.Result.Manifests, head.Result.Manifests), failures)
	return diff.Render(diff.RenderInput{
		PRNumber:     pr.Number,
		HeadSHA:      pr.HeadSHA,
		Changes:      changes,
		Images:       imageChanges(changes),
		Failures:     failures,
		MaxResources: e.maxDiffResources,
	})
}

// renderUsable reports whether a RenderTrees outcome yields a diff worth
// returning. flate's err is advisory (see RenderTrees), so this gates on what's
// actually usable rather than on err being nil:
//   - A nil Result is a fatal Bootstrap error — that side never rendered, so
//     there is no diff to show.
//   - A cancelled or timed-out context (konflate's DiffTimeout) left the render
//     incomplete: resources that never reconciled would read as missing, so a
//     partial diff would mislead.
//   - Otherwise a non-nil err is per-resource reconcile failures: the diff is
//     real and those failures surface via renderFailures, so proceed.
func renderUsable(base, head orchestrator.Rendered, err error) bool {
	if base.Result == nil || head.Result == nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// renderCfg is the flate render tuning shared by both sides of a
// RenderTrees comparison (Path / RepoRoot / PathOrig / SelfURLs are set
// from the per-side Tree). Two flags harden it for a tool that may be
// exposed:
//   - WipeSecrets replaces Secret cleartext with placeholders, so no secret
//     value can reach the diff (and thus a response) even if a manifest
//     carries one in cleartext.
//   - AllowMissingSecrets lets public/token-less renders proceed without the
//     cluster's real secrets (missing auth secrets become skips).
//   - GitDepth bounds GitRepository source clones to a shallow single commit.
//     A PR can declare an arbitrary GitRepository source, and konflate may
//     watch a repo it doesn't own; without a depth cap a hostile or huge source
//     would clone its full history (disk/CPU exhaustion). Matches flate's own
//     CLI default; commit-pinned refs still full-clone as flate requires.
//
// e.cache is the long-lived source cache reused across every PR; CacheDir
// roots flate's persistent stage + on-disk Helm render caches on the same
// (operator-mounted) volume so they survive restarts.
func (e *flateEngine) renderCfg() orchestrator.Config {
	return orchestrator.Config{
		SourceCache:            e.cache,
		CacheDir:               e.cacheDir,
		WipeSecrets:            true,
		AllowMissingSecrets:    true,
		GitDepth:               1,
		StageCacheBytes:        e.stageCacheBytes,
		HelmTemplateCacheBytes: e.helmTemplateCacheBytes,
		HelmRenderCacheBytes:   e.helmRenderCacheBytes,
		Concurrency:            e.concurrency,
		SourceRetry:            e.sourceRetry,
	}
}

func joinPath(dir, sub string) string {
	if sub == "" {
		return dir
	}
	return dir + "/" + sub
}

// pairChanges diffs two flate render outputs (each keyed by producing
// parent) into konflate's resource-level changes. The pairing,
// normalization, equality drop, and chart-label capture are flate's
// diff.Changes; konflate layers only its secret/cert scrub (the redact
// hook) on top of flate's default strip lists. flate.Change maps 1:1 onto
// our diff.Change (Parent struct → "Kind ns/name" label), already sorted
// by parent then identity.
//
// flate's equality is typed (reflect.DeepEqual over the normalized values),
// not byte equality, so a pair differing only by Go type — replicas: 3 (int)
// vs 3.0 (float64) — survives as "changed" even though it marshals to the same
// YAML. diff.Render collapses those type-only no-ops after marshaling, so they
// don't show as empty changed resources.
func pairChanges(base, head map[manifest.NamedResource][]map[string]any) []diff.Change {
	fc := flatediff.Changes(
		flatediff.DocsFromManifests(base, nil),
		flatediff.DocsFromManifests(head, nil),
		flatediff.Options{
			StripAttrs:  flatediff.DefaultStripAttrs,
			StripFields: flatediff.DefaultStripFields,
			Normalize:   redact,
		},
	)
	out := make([]diff.Change, len(fc))
	for i, c := range fc {
		out[i] = diff.Change{
			Status:    string(c.Status),
			Kind:      c.Kind,
			Namespace: c.Namespace,
			Name:      c.Name,
			Parent:    parentLabel(manifest.NamedResource{Kind: c.Parent.Kind, Namespace: c.Parent.Namespace, Name: c.Parent.Name}),
			Old:       c.Old,
			New:       c.New,
			OldChart:  c.OldChart,
			NewChart:  c.NewChart,
		}
	}
	return out
}

// unionKeys returns the deduplicated keys present across the given maps.
func unionKeys[K comparable, V any](ms ...map[K]V) []K {
	set := make(map[K]struct{})
	for _, m := range ms {
		for k := range m {
			set[k] = struct{}{}
		}
	}
	out := make([]K, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// redact is the konflate-specific scrub passed to flate's diff.Changes as
// Options.Normalize. flate already strips the chart-bump attributes and
// render-clock spec fields (via DefaultStripAttrs / DefaultStripFields)
// and summarizes ConfigMap binaryData; this adds the two scrubs flate
// leaves to the consumer's policy, run on the deep copy before flate's
// equality check so they never read as a change (nor reach a
// response):
//
//   - Secret data/stringData values — opaque and often render-random or
//     sensitive; whether keys were added/removed still shows.
//   - PEM cert/key material anywhere (a chart-minted TLS cert, a CRD/
//     webhook caBundle injected at render time) — different on every
//     render; flate only wipes the Secrets it reads for auth, not the ones
//     a chart renders into output.
func redact(m map[string]any) {
	if manifest.DocKind(m) == manifest.KindSecret {
		redactSecretValues(m)
	}
	redactCerts(m)
}

// redactSecretValues replaces every data / stringData value with a constant
// placeholder. A Secret's values are never the review signal — whether keys
// were added, removed, or renamed still shows, but the opaque (and often
// render-random or sensitive) value itself is suppressed. This also means a
// chart-generated credential that rotates every render no longer reads as a
// change, and no secret value is ever surfaced in the UI regardless of origin.
func redactSecretValues(m map[string]any) {
	for _, field := range [...]string{"data", "stringData"} {
		if kv, ok := m[field].(map[string]any); ok {
			for k := range kv {
				kv[k] = "<redacted>"
			}
		}
	}
}

// redactCerts walks the manifest and collapses any PEM certificate or key
// (verbatim or base64-wrapped) to a stable token. Cert material outside Secrets
// — a CRD's spec.conversion.webhook.clientConfig.caBundle, a webhook
// configuration's caBundle, an APIService's caBundle — is injected at render
// time (cert-manager ca-injector, or Helm genSignedCert) and differs on every
// render, so a verbatim diff is pure noise on any PR that re-renders the chart.
func redactCerts(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = redactCerts(val)
		}
	case []any:
		for i, val := range t {
			t[i] = redactCerts(val)
		}
	case string:
		if token, ok := certToken(t); ok {
			return token
		}
	}
	return v
}

// certToken returns a stable placeholder for a PEM cert/key string (raw or
// base64-wrapped), and whether s was one. The cheap prefix guards keep the
// common case — a string that is neither — from paying for a base64 decode.
func certToken(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(trimmed, "-----BEGIN "):
		return pemToken(trimmed)
	case strings.HasPrefix(trimmed, "LS0tLS1"): // base64 of "-----B…"
		if dec, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
			return pemToken(strings.TrimSpace(string(dec)))
		}
	}
	return "", false
}

// pemToken maps a "-----BEGIN <type>-----" header to a human token. The body is
// deliberately discarded: two certs for the same subject still differ byte-for-
// byte every render, so only the kind of material is worth showing.
func pemToken(pem string) (string, bool) {
	header, _, ok := strings.Cut(pem, "\n")
	if !ok {
		header = pem
	}
	switch {
	case !strings.HasPrefix(header, "-----BEGIN "):
		return "", false
	case strings.Contains(header, "PRIVATE KEY"):
		return "<private key>", true
	case strings.Contains(header, "CERTIFICATE REQUEST"):
		return "<certificate request>", true
	case strings.Contains(header, "CERTIFICATE"):
		return "<certificate>", true
	case strings.Contains(header, "PUBLIC KEY"):
		return "<public key>", true
	}
	return "", false
}

// renderFailures converts flate's per-parent render failures into the API shape,
// unioning the base and head sides (a source can time out on either) and
// deduping a parent that failed on both (first side's message wins). konflate
// also drops the failed parents' resources from the diff (see dropFailedParents),
// so this list is the only place such a parent surfaces.
func renderFailures(sides ...map[manifest.NamedResource]store.StatusInfo) []api.RenderFailure {
	msgByParent := map[string]string{}
	for _, failed := range sides {
		for nr, info := range failed {
			if label := parentLabel(nr); msgByParent[label] == "" {
				msgByParent[label] = info.Message
			}
		}
	}
	if len(msgByParent) == 0 {
		return nil
	}
	out := make([]api.RenderFailure, 0, len(msgByParent))
	for label, msg := range msgByParent {
		out = append(out, api.RenderFailure{Parent: label, Message: msg})
	}
	slices.SortFunc(out, func(a, b api.RenderFailure) int {
		return cmp.Or(cmp.Compare(a.Parent, b.Parent), cmp.Compare(a.Message, b.Message))
	})
	return out
}

// dropFailedParents removes changes attributed to a parent that failed to render.
// That parent produced no manifests on the failed side, so its resources would
// otherwise pair as phantom adds/removes — e.g. a transient OCI/source timeout
// showing as "every CRD removed" with false data-loss cautions. The failure is
// reported on its own (renderFailures); these bogus changes — and the cautions
// and image diffs computed from them — are dropped.
func dropFailedParents(changes []diff.Change, failures []api.RenderFailure) []diff.Change {
	if len(failures) == 0 {
		return changes
	}
	failed := make(map[string]bool, len(failures))
	for _, f := range failures {
		failed[f.Parent] = true
	}
	return slices.DeleteFunc(changes, func(c diff.Change) bool { return failed[c.Parent] })
}

func parentLabel(nr manifest.NamedResource) string {
	return nr.Kind + " " + nsName(nr.Namespace, nr.Name)
}

func resourceLabel(c diff.Change) string {
	return c.Kind + " " + nsName(c.Namespace, c.Name)
}

func nsName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}
