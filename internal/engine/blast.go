package engine

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/store"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/diff"
)

// blastRadiusTopN bounds how many parents the blast-radius block ranks, so a
// sweeping PR that touches a whole storage layer doesn't emit an unbounded list.
// The highest-impact parents (most transitive dependents) are kept.
const blastRadiusTopN = 10

// parentSet returns the producing parents on one side (the keys of a render's
// Manifests map: every Kustomization/HelmRelease that rendered, even to zero
// docs) — the authoritative "this parent exists on this side" set.
func parentSet(manifests map[manifest.NamedResource][]map[string]any) map[manifest.NamedResource]struct{} {
	out := make(map[manifest.NamedResource]struct{}, len(manifests))
	for id := range manifests {
		out[id] = struct{}{}
	}
	return out
}

// parentLabels maps each rendered parent's "Kind ns/name" label back to its
// flate id, so changes (which carry only the label) can be joined against the
// NamedResource-keyed dependsOn graph without parsing labels.
func parentLabels(sides ...map[manifest.NamedResource][]map[string]any) map[string]manifest.NamedResource {
	out := map[string]manifest.NamedResource{}
	for _, m := range sides {
		for id := range m {
			out[parentLabel(id)] = id
		}
	}
	return out
}

// blastSeeds returns the parents whose reconciliation a reviewer cares about for
// blast-radius: those whose rendered output changed (a distinct Parent in the
// paired changes) plus those that failed to render on either side. labelToParent
// maps a change's "Kind ns/name" parent label back to the flate id.
func blastSeeds(
	changes []diff.Change,
	labelToParent map[string]manifest.NamedResource,
	failed ...map[manifest.NamedResource]store.StatusInfo,
) map[manifest.NamedResource]struct{} {
	seeds := map[manifest.NamedResource]struct{}{}
	for _, c := range changes {
		if id, ok := labelToParent[c.Parent]; ok {
			seeds[id] = struct{}{}
		}
	}
	for _, side := range failed {
		for id := range side {
			seeds[id] = struct{}{}
		}
	}
	return seeds
}

// blastRadius ranks seeds by their downstream dependents in the post-merge
// (head) declared dependsOn graph. dependsOn is head.Result.DependsOn
// (dependent → its dependencies, same-kind only). It inverts the graph into a
// dependents adjacency list, then for each seed counts the direct dependents and
// the full transitive closure (BFS over reverse edges, cycle-safe via a visited
// set — flate also pre-validates dependsOn cycles). Only seeds with at least one
// dependent are returned; sorted by transitive count desc (then parent label)
// and capped to blastRadiusTopN.
func blastRadius(
	seeds map[manifest.NamedResource]struct{},
	dependsOn map[manifest.NamedResource][]manifest.NamedResource,
) []api.BlastRadiusEntry {
	if len(seeds) == 0 || len(dependsOn) == 0 {
		return nil
	}
	dependents := make(map[manifest.NamedResource][]manifest.NamedResource, len(dependsOn))
	for dependent, deps := range dependsOn {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], dependent)
		}
	}

	var out []api.BlastRadiusEntry
	for seed := range seeds {
		direct := dependents[seed]
		if len(direct) == 0 {
			continue
		}
		visited := map[manifest.NamedResource]struct{}{seed: {}}
		queue := slices.Clone(direct)
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if _, seen := visited[n]; seen {
				continue
			}
			visited[n] = struct{}{}
			queue = append(queue, dependents[n]...)
		}
		labels := make([]string, 0, len(direct))
		for _, d := range direct {
			labels = append(labels, parentLabel(d))
		}
		slices.Sort(labels)
		labels = slices.Compact(labels)
		out = append(out, api.BlastRadiusEntry{
			Parent:     parentLabel(seed),
			Direct:     labels,
			Transitive: len(visited) - 1, // exclude the seed itself
		})
	}
	slices.SortFunc(out, func(a, b api.BlastRadiusEntry) int {
		return cmp.Or(cmp.Compare(b.Transitive, a.Transitive), cmp.Compare(a.Parent, b.Parent))
	})
	if len(out) > blastRadiusTopN {
		out = out[:blastRadiusTopN]
	}
	return out
}

// danglingDependsOn flags parents the PR REMOVES (a key in the base render's
// parent set, absent from the head's) that surviving resources still declare a
// spec.dependsOn on — a reconciliation those dependents will wedge on after
// merge. headDependsOn is head.Result.DependsOn (dependent → its dependencies).
// A dependency that never rendered (a typo / always-missing target) is out of
// scope — only genuine removals are flagged. One caution per removed-and-still-
// referenced parent.
func danglingDependsOn(
	baseParents, headParents map[manifest.NamedResource]struct{},
	headDependsOn map[manifest.NamedResource][]manifest.NamedResource,
) []api.Warning {
	byRemoved := map[manifest.NamedResource][]manifest.NamedResource{}
	for dependent, deps := range headDependsOn {
		for _, dep := range deps {
			if _, stillRendered := headParents[dep]; stillRendered {
				continue // the dependency still exists in head — not dangling
			}
			if _, wasRendered := baseParents[dep]; !wasRendered {
				continue // never rendered (typo / always-missing) — not a removal
			}
			byRemoved[dep] = append(byRemoved[dep], dependent)
		}
	}
	if len(byRemoved) == 0 {
		return nil
	}
	out := make([]api.Warning, 0, len(byRemoved))
	for removed, deps := range byRemoved {
		labels := make([]string, 0, len(deps))
		for _, d := range deps {
			labels = append(labels, parentLabel(d))
		}
		slices.Sort(labels)
		labels = slices.Compact(labels)
		out = append(out, api.Warning{
			Level:    api.LevelCaution,
			Rule:     "dangling-dependson",
			Resource: parentLabel(removed),
			Detail: fmt.Sprintf("removed, but still declared in spec.dependsOn by %s — those will wedge on the missing dependency",
				strings.Join(labels, ", ")),
		})
	}
	slices.SortFunc(out, func(a, b api.Warning) int { return cmp.Compare(a.Resource, b.Resource) })
	return out
}
