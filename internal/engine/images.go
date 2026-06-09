package engine

import (
	"cmp"
	"slices"
	"strings"

	"github.com/home-operations/flate/pkg/image"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/diff"
)

// imageChanges derives the set of container-image changes from a set of
// resource changes. For each changed resource it compares the images in the
// old manifest with those in the new one (per image repository), and records
// the from→to transition along with every resource that references it. Added
// resources contribute pure additions (From == ""), removed resources pure
// removals (To == ""). The result is aggregated across resources and sorted.
func imageChanges(changes []diff.Change) []api.ImageChange {
	// key: repo + "\x00" + from + "\x00" + to → set of referencing resources.
	type agg struct {
		name, from, to string
		refs           map[string]struct{}
	}
	byKey := map[string]*agg{}

	record := func(repo, from, to, ref string) {
		if repo == "" || from == to {
			return
		}
		k := strings.Join([]string{repo, from, to}, "\x00")
		a := byKey[k]
		if a == nil {
			a = &agg{name: repo, from: from, to: to, refs: map[string]struct{}{}}
			byKey[k] = a
		}
		a.refs[ref] = struct{}{}
	}

	for _, c := range changes {
		ref := resourceLabel(c)
		oldImgs := collectImages(c.Old)
		newImgs := collectImages(c.New)
		for _, repo := range unionKeys(oldImgs, newImgs) {
			for _, t := range repoTransitions(oldImgs[repo], newImgs[repo]) {
				record(repo, t.from, t.to, ref)
			}
		}
	}

	out := make([]api.ImageChange, 0, len(byKey))
	for _, a := range byKey {
		refs := make([]string, 0, len(a.refs))
		for r := range a.refs {
			refs = append(refs, r)
		}
		slices.Sort(refs)
		out = append(out, api.ImageChange{Name: a.name, From: a.from, To: a.to, Refs: refs})
	}
	slices.SortFunc(out, func(a, b api.ImageChange) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	return out
}

// repoTransition is one before→after move of a single image repository; an empty
// from is a pure addition, an empty to a pure removal.
type repoTransition struct{ from, to string }

// repoTransitions pairs the before/after versions of one image repository into
// from→to moves. The everyday case — exactly one version on each side — is the
// familiar tag bump (old → new). When a repository appears at several versions
// at once (the same image in a main and an init container on different tags),
// pairing is ambiguous, so the symmetric difference is reported as discrete
// removals (to == "") and additions (from == "") rather than inventing a
// transition no container underwent — e.g. deleting an init container that
// pinned a newer tag must not read as a downgrade of the main container.
// Versions present on both sides are unchanged and yield nothing.
func repoTransitions(oldVers, newVers []string) []repoTransition {
	removed := setDiff(oldVers, newVers)
	added := setDiff(newVers, oldVers)
	if len(removed) == 1 && len(added) == 1 {
		return []repoTransition{{from: removed[0], to: added[0]}}
	}
	out := make([]repoTransition, 0, len(removed)+len(added))
	for _, v := range removed {
		out = append(out, repoTransition{from: v})
	}
	for _, v := range added {
		out = append(out, repoTransition{to: v})
	}
	return out
}

// setDiff returns the sorted elements of a that are absent from b.
func setDiff(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, v := range b {
		inB[v] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if _, ok := inB[v]; !ok {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// collectImages returns a map of image repository → the distinct versions (tags
// or digests) it is referenced at in a manifest. A repository may appear at more
// than one version at once — the same image used by a main and an init container
// on different tags — so versions are collected as a set per repository rather
// than collapsed to a single (arbitrary, last-write-wins) value, which would let
// imageChanges report a from→to transition no container actually underwent.
//
// The detection is flate's image.Extract — value-based, so it finds references
// anywhere in the tree (not just under containers[]/initContainers[]: CNPG
// spec.imageName, a CRD default, a sidecar under an arbitrary field) — and
// image.Split separates each into (repository, version) robustly (a digest beats
// a tag; a registry port is not mistaken for the version). nil manifest yields
// an empty map.
func collectImages(m map[string]any) map[string][]string {
	seen := map[string]map[string]struct{}{}
	for _, ref := range image.Extract(m) {
		repo, ver := image.Split(ref)
		if seen[repo] == nil {
			seen[repo] = map[string]struct{}{}
		}
		seen[repo][ver] = struct{}{}
	}
	out := make(map[string][]string, len(seen))
	for repo, vers := range seen {
		list := make([]string, 0, len(vers))
		for v := range vers {
			list = append(list, v)
		}
		slices.Sort(list)
		out[repo] = list
	}
	return out
}
