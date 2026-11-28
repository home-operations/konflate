package engine

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/orchestrator"

	"github.com/home-operations/konflate/internal/api"
)

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

// headDependsOnGraph returns the dependsOnGraph of the run's head side. The
// Kustomizations and HelmReleases come from flate's selection of the head
// tree, which lends them until the run closes. When flate renders
// changed-only, the selection holds only the parents in the namespaces the
// change touches, so the graph lacks the dependsOn of parents elsewhere, and
// of an unchanged HelmRelease flate did not load. Selection restates the render
// error for the selected resources; those failures already reach the diff as
// render failures, so only a selection flate could not make at all is an error.
func headDependsOnGraph(run *orchestrator.Run) (map[manifest.NamedResource][]manifest.NamedResource, error) {
	out := map[manifest.NamedResource][]manifest.NamedResource{}
	for _, rendered := range run.Results {
		if rendered.ID.Side != manifest.TreeHead {
			continue
		}
		selected, projection := run.Select([]manifest.TreeID{rendered.ID}, []manifest.NamedResource{
			{Group: manifest.FluxKustomizeDomain, Kind: manifest.KindKustomization},
			{Group: manifest.HelmReleaseDomain, Kind: manifest.KindHelmRelease},
		}, nil)
		if selected == nil {
			return nil, fmt.Errorf("select the head parents: %w", projection.Selected)
		}
		for _, selection := range selected {
			maps.Copy(out, dependsOnGraph(selection.Objects))
		}
	}
	return out, nil
}

// dependsOnGraph rebuilds the declared spec.dependsOn graph (dependent → its
// dependencies) from the Kustomizations and HelmReleases of a rendered tree.
// flate resolves a reference's omitted namespace to the dependent's own
// (Flux's default), and dependsOn is same-kind only (KS→KS, HR→HR per the
// Flux spec); implicit coupling (healthChecks, shared CRDs, service DNS) is
// not represented. Nodes with no declared dependencies are omitted. The graph
// is the declared one verbatim: cycle members and self-edges are retained
// (flate reports cycles as render failures), so a consumer walking it must
// tolerate them.
func dependsOnGraph(objects []manifest.BaseManifest) map[manifest.NamedResource][]manifest.NamedResource {
	out := map[manifest.NamedResource][]manifest.NamedResource{}
	for _, obj := range objects {
		var deps []manifest.NamedResource
		for ref := range manifest.References(obj, manifest.ReferenceRelations) {
			if ref.Use == "dependsOn" {
				deps = append(deps, ref.NamedResource)
			}
		}
		if len(deps) > 0 {
			out[obj.Named()] = deps
		}
	}
	return out
}

// sortByResource orders cautions by their "Kind ns/name" resource label so a
// render's warnings are deterministic regardless of map iteration order.
func sortByResource(w []api.Warning) {
	slices.SortFunc(w, func(a, b api.Warning) int { return cmp.Compare(a.Resource, b.Resource) })
}

// danglingDependsOn flags parents the PR REMOVES (a key in the base render's
// parent set, absent from the head's) that surviving resources still declare a
// spec.dependsOn on — a reconciliation those dependents will wedge on after
// merge. headDependsOn is the head side's dependsOnGraph (dependent → its
// dependencies).
// A dependency that never rendered (a typo / always-missing target) is out of
// scope — only genuine removals are flagged. One caution per removed-and-still-
// referenced parent. A dependent missing from the graph (see
// headDependsOnGraph) is not counted, so the caution can under-report.
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
			Detail: fmt.Sprintf("removed, but still declared in spec.dependsOn by %s; those will wedge on the missing dependency",
				strings.Join(labels, ", ")),
		})
	}
	sortByResource(out)
	return out
}

// staleValues surfaces flate's WarnStaleValues advisories as cautions: a
// HelmRelease sets top-level values the chart's templates no longer read — a key
// the chart removed or renamed, whose override now silently does nothing. It diffs
// head against base so only values *this PR* newly strands are flagged, not a
// pre-existing dead override (flate renders changed-only, so the advisories are
// already scoped to the PR's closure). flate v0.4.6+.
func staleValues(base, head []manifest.Warning) []api.Warning {
	wasStale := map[manifest.NamedResource]map[string]bool{}
	for _, w := range base {
		if w.Category != manifest.WarnStaleValues {
			continue
		}
		keys := wasStale[w.Resource]
		if keys == nil {
			keys = map[string]bool{}
			wasStale[w.Resource] = keys
		}
		for _, k := range w.Detail {
			keys[k] = true
		}
	}
	var out []api.Warning
	for _, w := range head {
		if w.Category != manifest.WarnStaleValues {
			continue
		}
		fresh := make([]string, 0, len(w.Detail))
		for _, k := range w.Detail {
			if !wasStale[w.Resource][k] {
				fresh = append(fresh, k)
			}
		}
		if len(fresh) == 0 {
			continue // every stale key here was already stale at the base — not this PR's doing
		}
		slices.Sort(fresh)
		noun := "values"
		if len(fresh) == 1 {
			noun = "a value"
		}
		out = append(out, api.Warning{
			Level:    api.LevelCaution,
			Rule:     "stale-helm-values",
			Resource: parentLabel(w.Resource),
			Detail: fmt.Sprintf("sets %s the chart no longer defines (%s); the override has no effect",
				noun, strings.Join(fresh, ", ")),
		})
	}
	sortByResource(out)
	return out
}
