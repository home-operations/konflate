package diff

import (
	"fmt"
	"slices"
	"strings"

	"github.com/home-operations/konflate/internal/api"
)

// Flux-semantic lint: the rules in this file reason about what Flux will
// actually do with a change on merge, not just what the change contains.
// They consume two inputs the generic rules don't have:
//
//   - The producing parent's own spec (ParentInfo, looked up by the engine
//     from the full rendered manifest sets): whether the Kustomization /
//     HelmRelease is suspended post-merge, and whether a Kustomization
//     prunes. A diff under a suspended parent won't roll out at all; a
//     removal under prune: false won't be deleted in-cluster.
//   - The parent docs themselves when the PR changes them: flipping
//     spec.suspend freezes or (scarier) unfreezes reconciliation.

// ParentInfo carries the Flux-semantic bits of one producing Kustomization /
// HelmRelease, keyed in Lint's parents map by the same "Kind ns/name" label
// Change.Parent uses. The engine looks the doc up in the rendered manifest
// sets — head side first (the post-merge truth), base side when the PR
// removes the parent itself. Found=false (the zero value) means the parent
// doc wasn't located; every rule below then stays quiet rather than guessing.
type ParentInfo struct {
	Found           bool
	IsKustomization bool
	Suspended       bool // spec.suspend (absent = false)
	Prune           bool // spec.prune, Kustomizations only (absent = false)
}

// fluxKind reports whether the change is a Flux Kustomization / HelmRelease
// document itself (guarded by API group — a non-Flux CRD that happens to be
// called "Kustomization" must not trip the suspend rules).
func fluxKind(c Change) bool {
	if c.Kind != "Kustomization" && c.Kind != "HelmRelease" {
		return false
	}
	for _, m := range []map[string]any{c.New, c.Old} {
		if m == nil {
			continue
		}
		if v, ok := m["apiVersion"].(string); ok {
			return strings.Contains(v, "toolkit.fluxcd.io")
		}
	}
	return false
}

// suspendToggleWarnings flags a PR flipping spec.suspend on a Kustomization /
// HelmRelease. Suspending freezes reconciliation; resuming is the sharper
// edge — everything that accumulated while suspended applies at once.
func suspendToggleWarnings(c Change) []api.Warning {
	if c.Status != "changed" || !fluxKind(c) {
		return nil
	}
	oldSusp := boolField(c.Old, "spec", "suspend")
	newSusp := boolField(c.New, "spec", "suspend")
	switch {
	case !oldSusp && newSusp:
		return []api.Warning{{
			Level: api.LevelCaution, Rule: "suspends", Resource: resourceLabel(c),
			Detail: "spec.suspend set — reconciliation freezes on merge; later changes will sit unapplied until it is resumed",
		}}
	case oldSusp && !newSusp:
		return []api.Warning{{
			Level: api.LevelCaution, Rule: "resumes", Resource: resourceLabel(c),
			Detail: "spec.suspend removed — reconciliation resumes on merge and every change accumulated while suspended applies at once",
		}}
	}
	return nil
}

// suspendedParentWarnings aggregates the changes whose producing parent is
// suspended post-merge: Flux will not reconcile any of them, so the diff
// shown is a diff of nothing-happens. One caution per suspended parent, not
// one per resource — a parked app shouldn't bury the review in repeats.
func suspendedParentWarnings(changes []Change, parents map[string]ParentInfo) []api.Warning {
	counts := map[string]int{}
	for _, c := range changes {
		if p := parents[c.Parent]; p.Found && p.Suspended {
			counts[c.Parent]++
		}
	}
	return aggregateParentWarnings(counts, "suspended-parent", func(parent string, n int) string {
		return fmt.Sprintf("%d %s under the suspended %s — Flux will not reconcile %s until it is resumed",
			n, plural(n, "changed resource", "changed resources"), parent, plural(n, "it", "them"))
	})
}

// notPrunedWarnings aggregates removals under a Kustomization with
// prune: false: Flux deletes nothing — the removed resources keep running
// in-cluster, unmanaged. That silent divergence is the signal; deletions
// under prune: true are ordinary GitOps (the dangerous kinds among them
// already carry their own cautions, sharpened by pruneSuffix).
func notPrunedWarnings(changes []Change, parents map[string]ParentInfo) []api.Warning {
	counts := map[string]int{}
	for _, c := range changes {
		if c.Status != statusRemoved {
			continue
		}
		if p := parents[c.Parent]; p.Found && p.IsKustomization && !p.Prune {
			counts[c.Parent]++
		}
	}
	return aggregateParentWarnings(counts, "not-pruned", func(parent string, n int) string {
		return fmt.Sprintf("%d removed %s under %s, which does not prune — Flux will leave %s running in-cluster, unmanaged",
			n, plural(n, "resource", "resources"), parent, plural(n, "it", "them"))
	})
}

// pruneSuffix sharpens a removed-resource caution with what Flux will
// actually do: under a pruning Kustomization the removal is a real in-cluster
// deletion on merge; under prune: false it is an orphaning. Empty when the
// parent is unknown or a HelmRelease (Helm deletes on upgrade either way, so
// the base wording already holds).
func pruneSuffix(c Change, parents map[string]ParentInfo) string {
	p := parents[c.Parent]
	if !p.Found || !p.IsKustomization {
		return ""
	}
	if p.Prune {
		return " (the Kustomization prunes: it is deleted in-cluster on merge)"
	}
	return " (the Kustomization does not prune: it is orphaned in-cluster, not deleted)"
}

// aggregateParentWarnings renders one warning per parent from a count map,
// sorted by parent label so the output is deterministic.
func aggregateParentWarnings(counts map[string]int, rule string, detail func(parent string, n int) string) []api.Warning {
	if len(counts) == 0 {
		return nil
	}
	labels := make([]string, 0, len(counts))
	for l := range counts {
		labels = append(labels, l)
	}
	slices.Sort(labels)
	out := make([]api.Warning, 0, len(labels))
	for _, l := range labels {
		out = append(out, api.Warning{
			Level: api.LevelCaution, Rule: rule, Resource: l, Detail: detail(l, counts[l]),
		})
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// boolField reads a boolean at the given path (false when missing or not a
// bool — Flux treats an absent suspend/prune as false, so the rules do too).
func boolField(m map[string]any, keys ...string) bool {
	if len(keys) == 0 {
		return false
	}
	parent, ok := nestedMap(m, keys[:len(keys)-1]...)
	if !ok {
		return false
	}
	b, _ := parent[keys[len(keys)-1]].(bool)
	return b
}
