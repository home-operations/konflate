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
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/orchestrator"

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

	// flate render tuning, resolved from config once (see internal/config). The
	// source retry defaults to flate's own CLI values; the reconcile
	// concurrency is bounded so a fan-out PR can't oversubscribe.
	concurrency      int
	sourceRetry      orchestrator.RetryConfig
	diffTimeout      time.Duration
	maxDiffResources int
	// kubeVersion overrides .Capabilities.KubeVersion for every chart rendered;
	// empty leaves helm's bundled version (see config.KubeVersion).
	kubeVersion string
	// helmAPIVersions extends .Capabilities.APIVersions for every chart
	// rendered; empty leaves helm's built-in set (see config.HelmAPIVersions).
	helmAPIVersions string
	// restrictEgress turns on flate's SSRF egress guard for source fetches (see
	// config.EgressRestricted): on by default while rendering untrusted fork PRs.
	restrictEgress bool
}

// New builds the production Engine from config. gitToken authenticates the
// renderer's clone/fetch with the same forge identity as the API client (a PAT or
// a GitHub App installation token; see provider.GitTokenSource) — nil clones
// anonymously, for public repositories only.
func New(cfg *config.Config, gitToken gitclone.TokenFunc) Engine {
	conc := cfg.RenderConcurrency
	if conc <= 0 {
		conc = runtime.NumCPU() * 4 // flate's default for I/O-bound reconcile work
	}
	return &flateEngine{
		clusterPath: cfg.ClusterPath,
		selfURLs:    []string{cfg.Forge.CloneURL()},
		cacheDir:    cfg.CacheDir,
		// The bare mirror is repo-specific, so it hangs off the per-repo subtree
		// (RepoCacheDir), not the shared CacheDir — otherwise a CacheDir volume
		// shared across different-repo instances would cross-fetch. flate's
		// content-addressed source cache (see renderCfg) stays on CacheDir and is
		// shared.
		mirror:      gitclone.NewMirror(cfg.RepoCacheDir, cfg.CloneDir, cfg.Forge.CloneURL(), gitToken, cfg.FetchTimeout),
		pullHeadRef: cfg.Forge.PullHeadRef,
		concurrency: conc,
		sourceRetry: orchestrator.RetryConfig{
			Attempts: cfg.SourceRetryAttempts,
			MinWait:  200 * time.Millisecond,
			MaxWait:  3 * time.Second,
			Jitter:   0.1,
		},
		diffTimeout:      cfg.DiffTimeout,
		maxDiffResources: cfg.MaxDiffResources,
		kubeVersion:      cfg.KubeVersion,
		helmAPIVersions:  cfg.HelmAPIVersions,
		restrictEgress:   cfg.EgressRestricted(),
	}
}

// Diff clones the PR, renders both sides with flate, and builds the diff. The
// whole operation is bounded by diffTimeout (when set) so one PR can't hold a
// render slot indefinitely — the deadline flows into the git fetch and both
// flate renders via ctx.
func (e *flateEngine) Diff(ctx context.Context, pr api.PR) (_ api.DiffResult, err error) {
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

	// flate's RenderTrees owns the two-sided render: given a base and a head
	// tree of the same cluster it detects the files that differ between them
	// and reconciles each side changed-only (only resources whose source files
	// differ, plus the dependency closure flate computes — not the whole
	// cluster, the dominant cold-start cost saver), sharing one source cache
	// so the head render reuses the base render's fetched charts / OCI layers /
	// git sources, concurrently.
	//
	// Each side is a Tree of its extracted repo root (RepoRoot — the anchor
	// repo-root-relative spec.path values resolve against) and the cluster's
	// Flux entry point within it (Path = root + clusterPath). Passing the root
	// explicitly is what makes clusterPath work: the trees have no .git, so
	// flate can't infer the root, and a repo-root-relative spec.path
	// (./kubernetes/...) would otherwise double under the entry-point subdir.
	// selfURLs lets a self-referential GitRepository alias to the tree. The
	// tree IDs name the comparison side; the run's results are sorted by
	// cluster and side and looked up per side (see collectSide) after
	// clusterIDs has confirmed the entry point holds a single cluster.
	//
	// Per flate's contract the returned err is advisory: a side is incomplete
	// only on a fatal discovery/bootstrap error, whereas per-resource
	// reconcile failures leave it complete and ARE the diff's render
	// failures — they must flow to renderFailures below, not abort the whole
	// diff (a PR that breaks a chart is exactly the one a reviewer needs to
	// see). renderUsable gates on that.
	run, err := orchestrator.RenderTrees(ctx, []orchestrator.Tree{
		{
			ID:       manifest.TreeID{Cluster: ".", Side: manifest.TreeBase},
			RepoRoot: clone.BaseDir, Path: joinPath(clone.BaseDir, e.clusterPath), SelfURLs: e.selfURLs,
		},
		{
			ID:       manifest.TreeID{Cluster: ".", Side: manifest.TreeHead},
			RepoRoot: clone.HeadDir, Path: joinPath(clone.HeadDir, e.clusterPath), SelfURLs: e.selfURLs,
		},
	}, e.renderCfg())
	if run == nil {
		return api.DiffResult{}, fmt.Errorf("engine: render PR #%d: %w", pr.Number, err)
	}
	// The run lends its documents and objects until Close, so it stays open
	// until the diff below has copied everything it keeps.
	defer func() {
		if closeErr := run.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("engine: close the render of PR #%d: %w", pr.Number, closeErr))
		}
	}()
	if !renderUsable(run.Results, err) {
		return api.DiffResult{}, fmt.Errorf("engine: render PR #%d: %w", pr.Number, err)
	}
	if clusters := clusterIDs(run.Results); len(clusters) > 1 {
		return api.DiffResult{}, fmt.Errorf(
			"engine: render PR #%d: cluster path %q holds %d clusters (%s); set KONFLATE_CLUSTER_PATH to one cluster's directory",
			pr.Number, e.clusterPath, len(clusters), strings.Join(clusters, ", "))
	}
	base, head := collectSide(run.Results, manifest.TreeBase), collectSide(run.Results, manifest.TreeHead)
	headDependsOn, err := headDependsOnGraph(run)
	if err != nil {
		return api.DiffResult{}, fmt.Errorf("engine: render PR #%d: %w", pr.Number, err)
	}

	// A parent (HelmRelease/Kustomization) whose render failed on a side produced
	// no manifests there, so pairing it against the other side reads as a wholesale
	// add/remove of all its resources — a transient OCI/source timeout looking like
	// "deleted every CRD" (with false data-loss cautions). Surface the failure, but
	// drop those phantom changes (and the cautions/image diffs derived from them) so
	// a timeout can't masquerade as a deletion.
	failures := renderFailures(base.failed, head.failed)
	changes := dropFailedParents(pairChanges(base.manifests, head.manifests), failures)
	result, err := diff.Render(diff.RenderInput{
		PRNumber:     pr.Number,
		HeadSHA:      pr.HeadSHA,
		Changes:      changes,
		Images:       imageChanges(changes),
		Failures:     failures,
		MaxResources: e.maxDiffResources,
		// Flux-semantic parent facts (suspend/prune) for the lint rules,
		// looked up across the full rendered sets — see parentInfos.
		Parents: parentInfos(base.manifests, head.manifests),
	})
	if err != nil {
		return api.DiffResult{}, err
	}

	// Attach the dependsOn-graph signal: a caution for any parent this PR
	// removes that surviving resources still dependOn. It is derived here (not
	// in diff.Render) because only the engine holds the flate run and the
	// declared graph it carries (see headDependsOnGraph).
	result.Warnings = append(result.Warnings, danglingDependsOn(
		parentSet(base.manifests), parentSet(head.manifests), headDependsOn)...)
	// Surface HelmReleases whose values the bumped chart no longer defines (flate's
	// WarnStaleValues), newly stranded by this PR — see staleValues.
	result.Warnings = append(result.Warnings, staleValues(base.warnings, head.warnings)...)
	return result, nil
}

// renderUsable reports whether a RenderTrees outcome yields a diff worth
// returning. flate's err is advisory (see RenderTrees), so this gates on what's
// actually usable rather than on err being nil:
//   - A run that is not Complete (or has no Result) failed fatally in
//     discovery/bootstrap — that side never rendered, so there is no diff to
//     show. A run flate marks Absent is the exception: it stands in for a
//     cluster present on only one side and carries an empty Result by design.
//   - A cancelled or timed-out context (konflate's DiffTimeout) left the render
//     incomplete: resources that never reconciled would read as missing, so a
//     partial diff would mislead.
//   - Otherwise a non-nil err is per-resource reconcile failures: the diff is
//     real and those failures surface via renderFailures, so proceed.
func renderUsable(runs []orchestrator.Rendered, err error) bool {
	if len(runs) == 0 {
		return false
	}
	for _, run := range runs {
		if run.Result == nil || (!run.Complete && !run.Absent) {
			return false
		}
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// clusterIDs returns the distinct clusters the runs cover, sorted. flate
// expands a tree into one run per cluster it discovers under the entry point
// (a repo root holding several clusters); konflate renders one cluster per
// instance and its parent labels carry no cluster, so same-named parents from
// two clusters (every cluster has a flux-system Kustomization) would collide
// in the diff. Diff rejects that case and points at KONFLATE_CLUSTER_PATH.
func clusterIDs(runs []orchestrator.Rendered) []string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		out = append(out, string(run.ID.Cluster))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// side is one comparison side's render output.
type side struct {
	// manifests is the rendered output keyed by the producing parent — every
	// Kustomization/HelmRelease that rendered on this side, even to zero docs.
	manifests map[manifest.NamedResource][]map[string]any
	failed    map[manifest.NamedResource]orchestrator.StatusInfo
	warnings  []manifest.Warning
}

// collectSide gathers the run of one comparison side (one per side once
// clusterIDs has ruled out a multi-cluster expansion).
func collectSide(runs []orchestrator.Rendered, which manifest.TreeSide) side {
	out := side{
		manifests: map[manifest.NamedResource][]map[string]any{},
		failed:    map[manifest.NamedResource]orchestrator.StatusInfo{},
	}
	for _, run := range runs {
		if run.ID.Side != which {
			continue
		}
		for _, batch := range run.Result.Manifests {
			var parent manifest.NamedResource
			if batch.Parent != nil {
				parent = batch.Parent.Named()
			}
			out.manifests[parent] = append(out.manifests[parent], batch.Documents...)
		}
		maps.Copy(out.failed, run.Result.Failed)
		out.warnings = append(out.warnings, run.Result.Warnings...)
	}
	return out
}

// renderCfg is the flate render tuning shared by both sides of a
// RenderTrees comparison (RepoRoot / Path / SelfURLs are set from the
// per-side Tree). Two settings harden it for a tool that may be exposed:
//   - AllowMissingKinds: Secret lets public/token-less renders proceed
//     without the cluster's real secrets (a missing auth or valuesFrom Secret
//     becomes a skip rather than a failure). flate itself never reads SOPS
//     ciphertext, and cleartext Secret data never reaches a response
//     regardless: konflate's own redact (see pairChanges) replaces every
//     Secret data/stringData value on the diff path.
//   - GitDepth bounds GitRepository source clones to a shallow single commit.
//     A PR can declare an arbitrary GitRepository source, and konflate may
//     watch a repo it doesn't own; without a depth cap a hostile or huge source
//     would clone its full history (disk/CPU exhaustion). Matches flate's own
//     CLI default; commit-pinned refs still full-clone as flate requires.
//
// HelmOptions carries only the capability overrides (see config.KubeVersion
// and config.HelmAPIVersions); the skip-* knobs stay off, since a reviewer
// needs every rendered resource.
//
// CacheDir roots flate's content-addressed source cache (fetched charts,
// OCI layers, git sources, persisted renders) on the operator-mounted volume
// so it survives restarts. flate opens the cache per RenderTrees call and
// concurrent diffs share one open owner, so the head render reuses what the
// base render fetched and a second PR reuses what the first did; it stays
// closed between diffs, which is when an operator's `flate cache gc` can take
// the store's exclusive lock.
func (e *flateEngine) renderCfg() orchestrator.Config {
	return orchestrator.Config{
		CacheDir:          e.cacheDir,
		AllowMissingKinds: []string{manifest.KindSecret},
		GitDepth:          1,
		HelmOptions:       orchestrator.HelmOptions{KubeVersion: e.kubeVersion, APIVersions: e.helmAPIVersions},
		Concurrency:       e.concurrency,
		SourceRetry:       e.sourceRetry,
		RestrictEgress:    e.restrictEgress,
	}
}

func joinPath(dir, sub string) string {
	if sub == "" {
		return dir
	}
	return dir + "/" + sub
}

// stripAttrs are the label/annotation keys removed from both sides before
// pairing: chart and checksum metadata whose values rotate on every chart bump
// and only duplicate the version or content change shown elsewhere. A trailing
// slash matches a prefix.
var stripAttrs = []string{"helm.sh/chart", "checksum/", "app.kubernetes.io/version", "chart"}

// pairChanges diffs two flate render outputs (each keyed by producing
// parent) into konflate's resource-level changes. Each document is paired
// with the document of the same identity under the same parent on the other
// side — so a resource moved between parents reads as a removal plus an
// addition, and a Deployment from one HelmRelease never matches the
// same-named Deployment of another. Both sides are compared on normalized
// copies (see normalize); the "helm.sh/chart" label is captured first, since
// normalization strips it. A later document shadows an earlier one of the
// same identity under one parent. Identity is exact, so a resource whose name
// carries a kustomize generator hash reads as a removal plus an addition when
// its content changes. The result is sorted by parent then identity.
//
// Equality is typed (reflect.DeepEqual over the normalized values), not byte
// equality, so a pair differing only by Go type — replicas: 3 (int) vs 3.0
// (float64) — survives as "changed" even though it marshals to the same YAML.
// diff.Render collapses those type-only no-ops after marshaling, so they
// don't show as empty changed resources.
func pairChanges(base, head map[manifest.NamedResource][]map[string]any) []diff.Change {
	type key struct{ parent, resource manifest.NamedResource }
	pairs := map[key]*diff.Change{}
	for i, side := range [...]map[manifest.NamedResource][]map[string]any{base, head} {
		for parent, docs := range side {
			for _, doc := range docs {
				normalized := normalize(doc)
				k := key{parent: parent, resource: manifest.DocIdentity(normalized)}
				c := pairs[k]
				if c == nil {
					c = &diff.Change{}
					pairs[k] = c
				}
				if i == 0 {
					c.Old, c.OldChart = normalized, chartLabel(doc)
				} else {
					c.New, c.NewChart = normalized, chartLabel(doc)
				}
			}
		}
	}
	keys := slices.SortedFunc(maps.Keys(pairs), func(a, b key) int {
		return cmp.Or(a.parent.Compare(b.parent), a.resource.Compare(b.resource))
	})
	out := make([]diff.Change, 0, len(keys))
	for _, k := range keys {
		c := pairs[k]
		switch {
		case c.Old == nil:
			c.Status = "added"
		case c.New == nil:
			c.Status = "removed"
		case reflect.DeepEqual(c.Old, c.New):
			continue
		default:
			c.Status = "changed"
		}
		c.Kind, c.Namespace, c.Name = k.resource.Kind, k.resource.Namespace, k.resource.Name
		c.Parent = parentLabel(k.parent)
		out = append(out, *c)
	}
	return out
}

// normalize returns the copy of a rendered document that pairChanges
// compares: stripAttrs removed, ConfigMap binaryData summarized, and
// konflate's secret/cert scrub (redact) applied. The copy also detaches the
// change from the document flate lends for the lifetime of the run.
func normalize(doc map[string]any) map[string]any {
	c := manifest.DeepCopyMap(doc)
	manifest.StripResourceAttributes(c, stripAttrs)
	if manifest.DocKind(c) == manifest.KindConfigMap {
		if binaryData, ok := c["binaryData"].(map[string]any); ok {
			for k, v := range binaryData {
				binaryData[k] = binaryDataSummary(v)
			}
		}
	}
	redact(c)
	return c
}

// binaryDataSummary replaces a ConfigMap binaryData value with its size and a
// content hash, so a changed blob still reads as a change without the diff
// carrying the base64 payload.
func binaryDataSummary(v any) string {
	s, ok := v.(string)
	if !ok {
		return "<redacted binary data>"
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return fmt.Sprintf("<redacted binary data: %d base64 chars sha256:%s>", len(s), manifest.SHA256Hex([]byte(s))[:16])
	}
	return fmt.Sprintf("<redacted binary data: %d bytes sha256:%s>", len(decoded), manifest.SHA256Hex(decoded)[:16])
}

// chartLabel returns a document's "helm.sh/chart" label ("<name>-<version>"),
// or "" when absent.
func chartLabel(doc map[string]any) string {
	meta, _ := doc["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	chart, _ := labels["helm.sh/chart"].(string)
	return chart
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

// redact is the secret/cert scrub normalize applies last. Both scrubs run
// on the deep copy before the equality check so they never read as a change
// (nor reach a response):
//
//   - Secret data/stringData values — opaque and often render-random or
//     sensitive; whether keys were added/removed still shows. This is the
//     load-bearing scrub: since flate 0.3.4 WipeSecrets covers only SOPS
//     ciphertext, so cleartext Secret values flow through the render and
//     are redacted here.
//   - PEM cert/key material anywhere (a chart-minted TLS cert, a CRD/
//     webhook caBundle injected at render time) — different on every
//     render; flate wipes SOPS material, not what a chart renders into
//     output.
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
func renderFailures(sides ...map[manifest.NamedResource]orchestrator.StatusInfo) []api.RenderFailure {
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
