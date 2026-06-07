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
			record(repo, oldImgs[repo], newImgs[repo], ref)
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

// collectImages returns a map of image repository → tag or digest for every
// container image referenced in a manifest. The detection is flate's
// image.Extract — value-based, so it finds references anywhere in the tree
// (not just under containers[]/initContainers[]: CNPG spec.imageName, a CRD
// default, a sidecar under an arbitrary field) — and image.Split separates
// each into (repository, version) robustly (a digest beats a tag; a
// registry port is not mistaken for the version). nil manifest yields an
// empty map.
func collectImages(m map[string]any) map[string]string {
	out := map[string]string{}
	for _, ref := range image.Extract(m) {
		repo, ver := image.Split(ref)
		out[repo] = ver
	}
	return out
}
