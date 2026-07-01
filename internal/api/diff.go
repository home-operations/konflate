// Package api defines the JSON types shared between the konflate server and
// the browser frontend. These types are the contract — change them carefully,
// as the TypeScript diff-viewer component is generated from them.
//
// Design principles:
//   - Every field carries only data the frontend consumes. Go-internal types
//     (template.HTML, template.CSS) are replaced with plain strings.
//   - Syntax-highlighted token spans are pre-rendered on the server as HTML
//     strings. The frontend inserts them via textContent of a container that
//     holds pre-escaped markup — never innerHTML of raw server data. The only
//     safe use is setting .innerHTML on a <td> whose content was produced by
//     chroma's HTML formatter (which HTML-escapes all token text).
//   - The tree hierarchy mirrors flate's treeParent → treeKind → treeItem
//     exactly so the frontend tree component has no remapping to do.
//   - Both unified and side-by-side rows are included in every DiffResource.
//     The frontend toggles visibility with CSS, not by re-fetching.
//   - Row/cell kinds ("ctx","add","del","blank") and resource statuses
//     ("changed","added","removed") are string constants so the TypeScript
//     types can use string literal unions without a code-generation step.
package api

// DiffResult is the top-level payload for GET /api/prs/{number}/diff.
// It is the JSON equivalent of flate's internal htmlData struct, with
// Go-specific types replaced by plain strings and the chroma stylesheet
// included so the frontend can inject it once per page load.
type DiffResult struct {
	// PRNumber is the forge PR number this diff describes.
	PRNumber int `json:"prNumber"`
	// HeadSHA is the git SHA of the PR head commit that was diffed.
	// The frontend displays it and uses it to detect stale cached diffs.
	HeadSHA string `json:"headSha"`

	// Summary counts changed/added/removed resources for the topbar pills.
	Summary DiffSummary `json:"summary"`

	// Impact is the blast-radius banner — the headline signal a rendered diff
	// gives that a raw file diff cannot (e.g. a two-line tag bump that removes
	// 50 resources).
	Impact Impact `json:"impact"`

	// BlastRadius ranks the changed/failed parents by how many downstream
	// parents declare a transitive spec.dependsOn on them — the reconciliation
	// blast radius a raw file diff can't show (a storage layer with 20
	// dependents vs a leaf app with none). Declared dependsOn only, same-kind
	// per the Flux spec; sorted by transitive count, top entries only. Empty
	// when nothing changed depends-on anything.
	BlastRadius []BlastRadiusEntry `json:"blastRadius,omitempty"`

	// Images lists container-image tag/digest changes across the rendered
	// workloads (flate's `diff images`) — usually the single most useful line
	// in a GitOps review: what actually gets deployed.
	Images []ImageChange `json:"images"`

	// Failures lists resources flate could not render on the head side
	// (orchestrator.Result.Failed/Orphans) — a PR that breaks templating
	// surfaces here, before merge, instead of failing in-cluster.
	Failures []RenderFailure `json:"failures"`

	// Warnings are heuristic flags over the rendered diff (data-loss, privilege,
	// RBAC, availability). Advisory by default (LevelCaution → a neutral check);
	// a LevelBlocking warning escalates the check to a failure. Every rule is a
	// caution today, so konflate stays a reviewer aid, not a gate.
	Warnings []Warning `json:"warnings"`

	// Routine is true when every changed resource differs only in container
	// image references and/or chart-version metadata (the helm.sh/chart /
	// app.kubernetes.io/version labels and Flux source version refs), with no
	// warnings and no render failures — i.e. an ordinary image/chart-version
	// bump. It is a property of the diff's *shape*, not a safety verdict:
	// konflate does not inspect what the new image does at runtime.
	Routine bool `json:"routine"`

	// ChromaCSS is the combined light+dark chroma stylesheet. Injected
	// once into a <style> tag on first diff load. Subsequent resource
	// selections do not re-inject it. Contains both .chroma.light and
	// .chroma.dark scoped rules so a single class toggle on <body> switches
	// the syntax highlight theme.
	ChromaCSS string `json:"chromaCss"`

	// Tree is the sidebar navigation: one entry per producing Flux object
	// (HelmRelease / Kustomization), each containing its changed Kinds,
	// each Kind containing its changed resources.
	Tree []DiffTreeParent `json:"tree"`

	// Resources holds every changed/added/removed resource. The tree items
	// reference resources by ID; the frontend fetches the matching Resource
	// from this slice when a tree leaf is selected.
	Resources []DiffResource `json:"resources"`

	// Truncated is the number of changed resources omitted because the diff
	// exceeded the render cap (KONFLATE_MAX_DIFF_RESOURCES) — a bound on the
	// memory and payload a single pathological/sweeping PR can cost. 0 means the
	// diff is complete. Summary and Impact still reflect the true totals; only
	// the per-resource render set is capped, so the UI warns the review is
	// partial.
	Truncated int `json:"truncated,omitempty"`
}

// DiffSummary carries the three counts shown in the topbar.
type DiffSummary struct {
	Changed int `json:"changed"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// Impact is the blast-radius summary rendered as a banner above the diff.
type Impact struct {
	Resources  int      `json:"resources"`  // total changed + added + removed
	Parents    int      `json:"parents"`    // distinct HelmReleases/Kustomizations touched
	Namespaces []string `json:"namespaces"` // namespaces with any change, sorted
	CRDs       int      `json:"crds"`       // CustomResourceDefinitions added/removed/changed
}

// BlastRadiusEntry is one changed (or failed) parent and its downstream
// dependents via declared spec.dependsOn. Direct lists the parents that depend
// on it directly ("Kind ns/name"); Transitive is the total number of dependents
// reachable through the dependsOn graph (direct + indirect). Only parents with
// at least one dependent appear, sorted by Transitive descending.
type BlastRadiusEntry struct {
	Parent     string   `json:"parent"`     // the changed/failed parent, "Kind ns/name"
	Direct     []string `json:"direct"`     // direct dependents, "Kind ns/name", sorted
	Transitive int      `json:"transitive"` // total dependents (direct + indirect)
}

// ImageChange is one container-image reference that changed between the
// merge-base and head renders. From == "" means newly introduced; To == ""
// means removed.
type ImageChange struct {
	Name string   `json:"name"` // image repository, e.g. "ghcr.io/rook/ceph"
	From string   `json:"from"` // old tag or digest ("" when added)
	To   string   `json:"to"`   // new tag or digest ("" when removed)
	Refs []string `json:"refs"` // resources referencing it, "Kind ns/name"
}

// RenderFailure is a resource flate could not render on the head side.
type RenderFailure struct {
	Parent  string `json:"parent"`  // producing HelmRelease/Kustomization, "Kind ns/name"
	Message string `json:"message"` // the render/reconcile error
}

// Level is a warning's severity tier. It is a string type so the JSON contract
// (and the generated TypeScript union) stays the plain value.
type Level string

const (
	// LevelCaution is advisory: the reviewer should look, but it does not block —
	// cautions map the PR's check to a neutral conclusion (see checkConclusion).
	// Every heuristic diff flag is a caution today.
	LevelCaution Level = "caution"
	// LevelBlocking escalates the PR's check to a failing conclusion — for a
	// finding that means the change would not deploy (e.g. an image tag missing
	// upstream). No rule emits it yet; it is the promotion target for such a rule.
	LevelBlocking Level = "blocking"
)

// Warning is one heuristic flag over the rendered diff. Rule is a stable machine
// id (e.g. "removed-statefulset", "privileged", "rbac-widened", "replicas-zero").
type Warning struct {
	Level    Level  `json:"level"`
	Rule     string `json:"rule"`
	Resource string `json:"resource"` // "Kind ns/name" the warning concerns
	Detail   string `json:"detail"`   // human-readable explanation
}

// WarningsByLevel returns the subset of ws at the given severity level,
// preserving order — used to render each tier in its own summary block and to
// count the per-tier [Signals].
func WarningsByLevel(ws []Warning, l Level) []Warning {
	out := make([]Warning, 0, len(ws))
	for _, w := range ws {
		if w.Level == l {
			out = append(out, w)
		}
	}
	return out
}

// DiffTreeParent is the top level of the sidebar tree: a Flux object
// (HelmRelease or Kustomization) and the kinds it produced that changed.
// Label example: "HelmRelease rook-ceph/rook-ceph"
// or "Kustomization rook-ceph/app (kubernetes/apps/rook-ceph/app)".
type DiffTreeParent struct {
	Label string         `json:"label"`
	Kinds []DiffTreeKind `json:"kinds"`
}

// DiffTreeKind groups resources by Kubernetes kind within a parent.
// Kind example: "ClusterRole", "Deployment".
type DiffTreeKind struct {
	Kind  string         `json:"kind"`
	Items []DiffTreeItem `json:"items"`
}

// DiffTreeItem is one leaf in the sidebar tree. ID references the matching
// DiffResource in DiffResult.Resources. Name is the ns/name label shown in
// the tree. Add and Del are the line-change counts shown as "+N -N" badges.
type DiffTreeItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // "changed" | "added" | "removed"
	Add    int    `json:"add"`
	Del    int    `json:"del"`
}

// DiffResource is one changed/added/removed Kubernetes resource, with both
// unified and side-by-side row sets pre-computed by the server. The frontend
// renders whichever set the current view toggle selects.
type DiffResource struct {
	// ID is the tree-leaf anchor, e.g. "r12". Stable within one DiffResult.
	ID string `json:"id"`
	// Title is the display heading, e.g. "Deployment rook-ceph/rook-ceph".
	Title string `json:"title"`
	// Kind is the Kubernetes kind, used for grouping in the tree.
	Kind string `json:"kind"`
	// Name is the namespace/name label, e.g. "rook-ceph/rook-ceph".
	Name string `json:"name"`
	// Parent is the producing Flux object label, same as DiffTreeParent.Label.
	Parent string `json:"parent"`
	// Status is "changed", "added", or "removed".
	Status string `json:"status"`
	// Add and Del are total changed-line counts for the tree badge.
	Add int `json:"add"`
	Del int `json:"del"`

	// Unified holds the rows for the single-column unified diff view.
	Unified []UnifiedRow `json:"unified"`
	// Side holds the rows for the two-column side-by-side diff view.
	Side []SideRow `json:"side"`
}

// UnifiedRow is one row in the single-column unified diff view.
//
// If Hunk is true this is a fold-separator row; all other fields except Fold
// and Count are empty. Clicking the separator expands the hidden context rows
// that share the same Fold id.
//
// If Folded is true this is a hidden context row that belongs to gap Fold; the
// frontend hides it until the expander is clicked.
//
// An ordinary visible row has Hunk=false, Folded=false.
type UnifiedRow struct {
	// Hunk marks a fold-separator (expander) row.
	Hunk bool `json:"hunk,omitempty"`
	// Folded marks a context row hidden behind the nearest expander.
	Folded bool `json:"folded,omitempty"`
	// Fold is the resource-local gap id shared by the expander and all
	// hidden rows belonging to the same gap, e.g. "g2".
	Fold string `json:"fold,omitempty"`
	// Count is the number of hidden lines, shown in the expander label.
	// Only non-zero on expander rows (Hunk=true).
	Count int `json:"count,omitempty"`

	// Kind is the row type: "ctx", "add", or "del".
	Kind string `json:"kind,omitempty"`
	// OldNo is the 1-based line number in the base file (0 = no number).
	OldNo int `json:"oldNo,omitempty"`
	// NewNo is the 1-based line number in the head file (0 = no number).
	NewNo int `json:"newNo,omitempty"`
	// HTML is a chroma-highlighted, HTML-escaped code span. The frontend
	// must set container.innerHTML (not textContent) for the spans to
	// render — but only because the content is server-produced chroma
	// output, which HTML-escapes all token text. See package doc note.
	HTML string `json:"html,omitempty"`
}

// SideRow is one row in the two-column side-by-side diff view.
//
// Hunk and Folded follow the same semantics as UnifiedRow.
type SideRow struct {
	Hunk   bool   `json:"hunk,omitempty"`
	Folded bool   `json:"folded,omitempty"`
	Fold   string `json:"fold,omitempty"`
	Count  int    `json:"count,omitempty"`

	Left  SideCell `json:"left"`
	Right SideCell `json:"right"`
}

// SideCell is one half of a SideRow.
type SideCell struct {
	// Kind is "ctx", "add", "del", or "blank" (absent line — shown as hatch).
	Kind string `json:"kind"`
	// No is the 1-based line number (0 = no number, i.e. blank cells).
	No int `json:"no,omitempty"`
	// HTML is a chroma-highlighted, HTML-escaped code span (see UnifiedRow.HTML).
	HTML string `json:"html,omitempty"`
}
