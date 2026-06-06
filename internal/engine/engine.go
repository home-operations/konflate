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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

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
	cacheDir    string
	// mirror is the persistent bare clone of the repo; each diff fetches the
	// base/head refs into it incrementally instead of re-cloning.
	mirror *gitclone.Mirror
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
		cacheDir:               cfg.CacheDir,
		mirror:                 gitclone.NewMirror(cfg.CacheDir, cfg.CloneDir, cfg.Forge.CloneURL(), cfg.Token),
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
	}
}

// Diff clones the PR, renders both sides with flate, and builds the diff.
func (e *flateEngine) Diff(ctx context.Context, pr api.PR) (api.DiffResult, error) {
	clone, err := e.mirror.Trees(ctx, pr.HeadRef, pr.BaseRef)
	if err != nil {
		return api.DiffResult{}, fmt.Errorf("engine: clone PR #%d: %w", pr.Number, err)
	}
	defer clone.Cleanup()

	baseTree := joinPath(clone.BaseDir, e.clusterPath)
	headTree := joinPath(clone.HeadDir, e.clusterPath)

	// Changed-only mode (flate's PathOrig): each side reconciles only the
	// resources whose source files differ between the merge-base and head trees,
	// plus the dependency closure flate computes — not the whole cluster. This is
	// the dominant cold-start cost saver: a one-file PR renders a handful of
	// resources instead of every HelmRelease/Kustomization. pairChanges over the
	// two scoped outputs yields the same diff a full render would (unchanged
	// resources never enter either side). Render the base first so its fetched
	// sources warm the shared cache for the head render.
	base, err := e.render(ctx, baseTree, headTree)
	if err != nil {
		return api.DiffResult{}, fmt.Errorf("engine: render base of PR #%d: %w", pr.Number, err)
	}
	head, err := e.render(ctx, headTree, baseTree)
	if err != nil {
		return api.DiffResult{}, fmt.Errorf("engine: render head of PR #%d: %w", pr.Number, err)
	}

	changes := pairChanges(base.Manifests, head.Manifests)
	return diff.Render(diff.RenderInput{
		PRNumber: pr.Number,
		HeadSHA:  pr.HeadSHA,
		Changes:  changes,
		Images:   imageChanges(changes),
		Failures: renderFailures(head.Failed),
	})
}

// render runs one flate orchestrator over the cluster at path in changed-only
// mode: origPath is the opposite (merge-base or head) tree, so flate reconciles
// only the resources whose source files differ between the two — plus the
// dependency closure it computes (substituteFrom producers, consumers of a
// changed source, etc.) — instead of the whole cluster. Render is flate's embed
// entry point (Bootstrap + Run + collect). Two flags harden it for a tool that
// may be exposed:
//   - WipeSecrets replaces Secret cleartext with placeholders, so no secret
//     value can reach the diff (and thus a response) even if a manifest carries
//     one in cleartext.
//   - AllowMissingSecrets lets public/token-less renders proceed without the
//     cluster's real secrets (missing auth secrets become skips).
func (e *flateEngine) render(ctx context.Context, path, origPath string) (*orchestrator.Result, error) {
	o, err := orchestrator.New(orchestrator.Config{
		Path:        path,
		PathOrig:    origPath,
		SourceCache: e.cache,
		// CacheDir roots flate's persistent stage + on-disk Helm render caches on
		// the same (operator-mounted) volume as the source cache, so they survive
		// restarts and are reused across PRs.
		CacheDir:               e.cacheDir,
		WipeSecrets:            true,
		AllowMissingSecrets:    true,
		StageCacheBytes:        e.stageCacheBytes,
		HelmTemplateCacheBytes: e.helmTemplateCacheBytes,
		HelmRenderCacheBytes:   e.helmRenderCacheBytes,
		Concurrency:            e.concurrency,
		SourceRetry:            e.sourceRetry,
	})
	if err != nil {
		return nil, err
	}
	defer o.Stop()
	return o.Render(ctx)
}

func joinPath(dir, sub string) string {
	if sub == "" {
		return dir
	}
	return dir + "/" + sub
}

// childEntry is one rendered resource together with the parent (HelmRelease /
// Kustomization) that produced it.
type childEntry struct {
	parent   manifest.NamedResource
	resource map[string]any
}

// pairChanges diffs two flate render outputs into resource-level changes.
// Manifests is keyed by the producing parent; each parent maps to the list of
// child resources it rendered. A child is matched across the two sides by
// (parent, kind, namespace, name); unchanged children are dropped. The result
// is sorted for deterministic output.
func pairChanges(base, head map[manifest.NamedResource][]map[string]any) []diff.Change {
	baseIdx := indexChildren(base)
	headIdx := indexChildren(head)

	keys := unionKeys(baseIdx, headIdx)
	changes := make([]diff.Change, 0, len(keys))
	for _, k := range keys {
		old, inBase := baseIdx[k]
		nw, inHead := headIdx[k]
		switch {
		case inBase && inHead:
			if reflect.DeepEqual(old.resource, nw.resource) {
				continue // unchanged
			}
			changes = append(changes, change("changed", nw.parent, old.resource, nw.resource))
		case inHead:
			changes = append(changes, change("added", nw.parent, nil, nw.resource))
		default:
			changes = append(changes, change("removed", old.parent, old.resource, nil))
		}
	}

	slices.SortFunc(changes, func(a, b diff.Change) int {
		return cmp.Or(
			cmp.Compare(a.Parent, b.Parent),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Namespace, b.Namespace),
			cmp.Compare(a.Name, b.Name),
		)
	})
	return changes
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

// stripAttrs are the metadata annotation/label keys dropped before diffing.
// They rotate on every Helm chart bump (the chart version, value checksums) and
// carry no review-relevant signal, so leaving them in surfaces a spurious
// change on every resource a touched chart renders — even resources the PR
// doesn't change. Mirrors flate's diff defaults.
var stripAttrs = []string{
	"helm.sh/chart",
	"checksum/config",
	"checksum/secret",
	"app.kubernetes.io/version",
	"chart",
}

// normalize rewrites a rendered manifest in place so render-time noise doesn't
// register as a change. flate renders each side of the diff fresh, so anything
// non-deterministic or review-irrelevant in the output would otherwise surface
// as a phantom change on every PR that touches a neighbouring chart. Three
// classes are scrubbed:
//
//   - Chart-bump attributes (helm.sh/chart, checksum/*, …) — stripped from
//     metadata and nested pod/CronJob/volumeClaimTemplate metadata. Mirrors
//     flate's own pre-diff normalization (manifest.StripResourceAttributes is
//     the routine its diff backend uses).
//   - ConfigMap binaryData — opaque base64 blobs collapsed to a content
//     summary; the review signal is "did the bytes change", not which base64
//     character flipped.
//   - Secret values and PEM cert/key material — see redactSecretValues /
//     redactCerts. Charts that mint a TLS cert at render time (Helm
//     genSignedCert) produce a different cert on every render; flate only wipes
//     the Secrets it reads for auth, not the ones a chart renders into output.
func normalize(m map[string]any) {
	manifest.StripResourceAttributes(m, stripAttrs)
	switch manifest.DocKind(m) {
	case manifest.KindConfigMap:
		summarizeBinaryData(m)
	case manifest.KindSecret:
		redactSecretValues(m)
	}
	redactCerts(m)
}

// summarizeBinaryData collapses each ConfigMap.binaryData value to a stable,
// content-derived placeholder (verbatim base64 diffs are gibberish to a
// reviewer and pathologically large for the renderer).
func summarizeBinaryData(m map[string]any) {
	bd, ok := m["binaryData"].(map[string]any)
	if !ok {
		return
	}
	for k, v := range bd {
		bd[k] = binaryDataSummary(v)
	}
}

func binaryDataSummary(v any) string {
	s, _ := v.(string)
	raw := []byte(s)
	if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s)); err == nil {
		raw = dec
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("<binary %d bytes, sha256:%s>", len(raw), hex.EncodeToString(sum[:])[:16])
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

// indexChildren flattens the parent→children map into a key→entry map, keyed by
// parent + child coordinates so a child is paired only with its counterpart
// under the same parent. Each manifest is normalized first.
func indexChildren(m map[manifest.NamedResource][]map[string]any) map[string]childEntry {
	idx := make(map[string]childEntry)
	for parent, children := range m {
		for _, res := range children {
			normalize(res)
			kind, ns, name := coords(res)
			key := strings.Join([]string{parentLabel(parent), kind, ns, name}, "\x00")
			idx[key] = childEntry{parent: parent, resource: res}
		}
	}
	return idx
}

func change(status string, parent manifest.NamedResource, old, nw map[string]any) diff.Change {
	src := nw
	if src == nil {
		src = old
	}
	kind, ns, name := coords(src)
	return diff.Change{
		Status:    status,
		Kind:      kind,
		Namespace: ns,
		Name:      name,
		Parent:    parentLabel(parent),
		Old:       old,
		New:       nw,
	}
}

// renderFailures converts flate's per-parent render failures into the API shape.
func renderFailures(failed map[manifest.NamedResource]store.StatusInfo) []api.RenderFailure {
	if len(failed) == 0 {
		return nil
	}
	out := make([]api.RenderFailure, 0, len(failed))
	for nr, info := range failed {
		out = append(out, api.RenderFailure{Parent: parentLabel(nr), Message: info.Message})
	}
	slices.SortFunc(out, func(a, b api.RenderFailure) int {
		return cmp.Or(cmp.Compare(a.Parent, b.Parent), cmp.Compare(a.Message, b.Message))
	})
	return out
}

// coords extracts kind/namespace/name from a manifest map.
func coords(m map[string]any) (kind, ns, name string) {
	kind, _ = m["kind"].(string)
	if meta, ok := m["metadata"].(map[string]any); ok {
		name, _ = meta["name"].(string)
		ns, _ = meta["namespace"].(string)
	}
	return kind, ns, name
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
