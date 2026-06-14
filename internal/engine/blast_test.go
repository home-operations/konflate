package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/store"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/diff"
)

func ks(name string) manifest.NamedResource {
	return manifest.NamedResource{Kind: manifest.KindKustomization, Namespace: "flux-system", Name: name}
}

func TestBlastRadius(t *testing.T) {
	base, app, web, db, cache, leaf := ks("base"), ks("app"), ks("web"), ks("db"), ks("cache"), ks("leaf")
	// app, web, db → base; cache → db. So base has 3 direct + cache transitively
	// (via db) = 4; db has 1 (cache); leaf has none.
	dependsOn := map[manifest.NamedResource][]manifest.NamedResource{
		app:   {base},
		web:   {base},
		db:    {base},
		cache: {db},
	}
	seeds := map[manifest.NamedResource]struct{}{base: {}, db: {}, leaf: {}}

	got := blastRadius(seeds, dependsOn)
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2 (leaf has no dependents, must be omitted): %+v", len(got), got)
	}

	// Sorted by transitive desc: base (4) before db (1).
	if got[0].Parent != parentLabel(base) || got[0].Transitive != 4 {
		t.Fatalf("entry[0] = %+v, want base with transitive 4", got[0])
	}
	wantDirect := []string{parentLabel(app), parentLabel(db), parentLabel(web)} // sorted labels
	if !slices.Equal(got[0].Direct, wantDirect) {
		t.Errorf("base direct = %v, want %v", got[0].Direct, wantDirect)
	}
	if got[1].Parent != parentLabel(db) || got[1].Transitive != 1 {
		t.Errorf("entry[1] = %+v, want db with transitive 1", got[1])
	}

	if blastRadius(nil, dependsOn) != nil {
		t.Error("no seeds → nil")
	}
	if blastRadius(map[manifest.NamedResource]struct{}{leaf: {}}, dependsOn) != nil {
		t.Error("seed with no dependents → nil")
	}
}

func TestBlastSeeds(t *testing.T) {
	a, b, f := ks("a"), ks("b"), ks("failed")
	labelToParent := parentLabels(map[manifest.NamedResource][]map[string]any{a: nil, b: nil, f: nil})
	changes := []diff.Change{
		{Parent: parentLabel(a)},
		{Parent: parentLabel(a)}, // duplicate parent — deduped by the set
		{Parent: parentLabel(b)},
		{Parent: "Kustomization other/unknown"}, // not a rendered parent — skipped
	}
	failed := map[manifest.NamedResource]store.StatusInfo{f: {}}

	got := blastSeeds(changes, labelToParent, failed)
	want := map[manifest.NamedResource]struct{}{a: {}, b: {}, f: {}}
	if !maps.Equal(got, want) {
		t.Errorf("seeds = %v, want %v", got, want)
	}
}

func TestDanglingDependsOn(t *testing.T) {
	base, app, db, other := ks("base"), ks("app"), ks("db"), ks("other")
	baseParents := map[manifest.NamedResource]struct{}{base: {}, app: {}, db: {}, other: {}}
	headParents := map[manifest.NamedResource]struct{}{app: {}, db: {}, other: {}} // base removed
	headDependsOn := map[manifest.NamedResource][]manifest.NamedResource{
		app: {base},        // base was removed → dangling
		db:  {base, other}, // base dangling; other still present → fine
	}

	got := danglingDependsOn(baseParents, headParents, headDependsOn)
	if len(got) != 1 {
		t.Fatalf("warnings = %d, want 1 (only base is removed-and-referenced): %+v", len(got), got)
	}
	w := got[0]
	if w.Rule != "dangling-dependson" || w.Resource != parentLabel(base) {
		t.Fatalf("warning = %+v, want dangling-dependson on base", w)
	}
	if !strings.Contains(w.Detail, parentLabel(app)) || !strings.Contains(w.Detail, parentLabel(db)) {
		t.Errorf("detail must name both dependents: %q", w.Detail)
	}
}

func TestDanglingDependsOn_Quiet(t *testing.T) {
	base, app, missing := ks("base"), ks("app"), ks("missing")

	// Dependency still rendered in head → not dangling.
	present := danglingDependsOn(
		map[manifest.NamedResource]struct{}{base: {}, app: {}},
		map[manifest.NamedResource]struct{}{base: {}, app: {}},
		map[manifest.NamedResource][]manifest.NamedResource{app: {base}},
	)
	if present != nil {
		t.Errorf("present dependency must not warn: %+v", present)
	}

	// Target that never rendered (typo / always-missing) → out of scope.
	never := danglingDependsOn(
		map[manifest.NamedResource]struct{}{app: {}},
		map[manifest.NamedResource]struct{}{app: {}},
		map[manifest.NamedResource][]manifest.NamedResource{app: {missing}},
	)
	if never != nil {
		t.Errorf("never-rendered target must not warn: %+v", never)
	}
}

func TestStaleValues(t *testing.T) {
	hr := func(n string) manifest.NamedResource {
		return manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: n}
	}
	stale := func(nr manifest.NamedResource, keys ...string) manifest.Warning {
		return manifest.Warning{Category: manifest.WarnStaleValues, Resource: nr, Detail: keys}
	}

	// Newly stale: head strands two keys the base render didn't.
	got := staleValues(nil, []manifest.Warning{stale(hr("plex"), "ingress.enabled", "foo")})
	if len(got) != 1 {
		t.Fatalf("warnings = %d, want 1: %+v", len(got), got)
	}
	if w := got[0]; w.Level != api.LevelCaution || w.Rule != "stale-helm-values" || w.Resource != parentLabel(hr("plex")) {
		t.Errorf("warning = %+v, want a stale-helm-values caution on plex", w)
	}
	if !strings.Contains(got[0].Detail, "ingress.enabled") || !strings.Contains(got[0].Detail, "foo") {
		t.Errorf("detail must list both stranded keys: %q", got[0].Detail)
	}

	// Only the newly-stranded key is flagged; one already stale at the base is not.
	got = staleValues(
		[]manifest.Warning{stale(hr("plex"), "old")},
		[]manifest.Warning{stale(hr("plex"), "old", "new")},
	)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "new") || strings.Contains(got[0].Detail, "old") {
		t.Errorf("want only the newly-stale key 'new', not pre-existing 'old': %+v", got)
	}

	// Staleness present on both sides → pre-existing, not this PR's doing → no caution.
	if got := staleValues(
		[]manifest.Warning{stale(hr("plex"), "old")},
		[]manifest.Warning{stale(hr("plex"), "old")},
	); got != nil {
		t.Errorf("pre-existing staleness must not warn: %+v", got)
	}

	// Other flate warning categories are ignored.
	if got := staleValues(nil, []manifest.Warning{{Category: "EmptyScan", Resource: hr("plex")}}); got != nil {
		t.Errorf("non-StaleValues categories must be ignored: %+v", got)
	}
}
