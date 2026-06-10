package diff

import (
	"strings"
	"testing"

	"github.com/home-operations/konflate/internal/api"
)

// fluxDoc builds a Flux Kustomization/HelmRelease manifest with the given
// spec, for the suspend-toggle rules that inspect the doc itself.
func fluxDoc(kind string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": map[string]string{
			"Kustomization": "kustomize.toolkit.fluxcd.io/v1",
			"HelmRelease":   "helm.toolkit.fluxcd.io/v2",
		}[kind],
		"kind": kind,
		"spec": spec,
	}
}

func TestFlux_SuspendToggles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"Kustomization", "HelmRelease"} {
		// false → true: freezes.
		c := Change{
			Status: "changed", Kind: kind, Namespace: "flux-system", Name: "apps",
			Old: fluxDoc(kind, map[string]any{}),
			New: fluxDoc(kind, map[string]any{"suspend": true}),
		}
		if n, w := countRule(Lint([]Change{c}, nil, nil), "suspends"); n != 1 || !strings.Contains(w.Detail, "freezes") {
			t.Errorf("%s suspend set: want 1 suspends caution, got %d (%q)", kind, n, w.Detail)
		}

		// true → absent: resumes, the accumulated-changes warning.
		c.Old, c.New = c.New, c.Old
		if n, w := countRule(Lint([]Change{c}, nil, nil), "resumes"); n != 1 || !strings.Contains(w.Detail, "accumulated") {
			t.Errorf("%s suspend removed: want 1 resumes caution, got %d (%q)", kind, n, w.Detail)
		}
	}

	// A non-Flux CRD that happens to be called Kustomization must not trip.
	argo := Change{
		Status: "changed", Kind: "Kustomization", Name: "x",
		Old: map[string]any{"apiVersion": "example.io/v1", "kind": "Kustomization", "spec": map[string]any{}},
		New: map[string]any{"apiVersion": "example.io/v1", "kind": "Kustomization", "spec": map[string]any{"suspend": true}},
	}
	ws := Lint([]Change{argo}, nil, nil)
	if hasRule(ws, "suspends") {
		t.Errorf("non-Flux Kustomization tripped the suspend rule: %+v", ws)
	}

	// An unrelated change on a Flux doc (no suspend flip) stays quiet.
	quiet := Change{
		Status: "changed", Kind: "HelmRelease", Name: "x",
		Old: fluxDoc("HelmRelease", map[string]any{"interval": "30m"}),
		New: fluxDoc("HelmRelease", map[string]any{"interval": "1h"}),
	}
	ws = Lint([]Change{quiet}, nil, nil)
	if hasRule(ws, "suspends") || hasRule(ws, "resumes") {
		t.Errorf("interval-only change tripped a suspend rule: %+v", ws)
	}
}

func TestFlux_SuspendedParentAggregates(t *testing.T) {
	t.Parallel()
	parents := map[string]ParentInfo{
		"Kustomization flux-system/media": {Found: true, IsKustomization: true, Suspended: true, Prune: true},
		"HelmRelease media/plex":          {Found: true, Suspended: false},
	}
	changes := []Change{
		{Status: "changed", Kind: "Deployment", Namespace: "media", Name: "radarr", Parent: "Kustomization flux-system/media", Old: map[string]any{}, New: map[string]any{}},
		{Status: "added", Kind: "ConfigMap", Namespace: "media", Name: "cfg", Parent: "Kustomization flux-system/media", New: map[string]any{}},
		{Status: "changed", Kind: "Deployment", Namespace: "media", Name: "plex", Parent: "HelmRelease media/plex", Old: map[string]any{}, New: map[string]any{}},
	}

	ws := Lint(changes, nil, parents)
	n, w := countRule(ws, "suspended-parent")
	if n != 1 {
		t.Fatalf("suspended-parent warnings = %d, want 1 (aggregated): %+v", n, ws)
	}
	if w.Resource != "Kustomization flux-system/media" || !strings.Contains(w.Detail, "2 changed resources") {
		t.Errorf("aggregate should name the parent and count 2, got %+v", w)
	}

	// No parent facts at all (nil map) → silent, never guessing.
	if hasRule(Lint(changes, nil, nil), "suspended-parent") {
		t.Error("suspended-parent fired without parent facts")
	}
}

func TestFlux_PruneSemanticsOnRemovals(t *testing.T) {
	t.Parallel()
	pruning := map[string]ParentInfo{
		"Kustomization flux-system/db": {Found: true, IsKustomization: true, Prune: true},
	}
	notPruning := map[string]ParentInfo{
		"Kustomization flux-system/db": {Found: true, IsKustomization: true, Prune: false},
	}
	removedSts := Change{
		Status: statusRemoved, Kind: "StatefulSet", Namespace: "default", Name: "postgres",
		Parent: "Kustomization flux-system/db", Old: map[string]any{},
	}

	// prune: true — the dangerous-kind caution states the deletion is real.
	ws := Lint([]Change{removedSts}, nil, pruning)
	if _, w := countRule(ws, "removed-statefulset"); !strings.Contains(w.Detail, "deleted in-cluster on merge") {
		t.Errorf("pruning parent: caution should state the deletion, got %q", w.Detail)
	}
	if hasRule(ws, "not-pruned") {
		t.Errorf("pruning parent must not raise not-pruned: %+v", ws)
	}

	// prune: false — the caution states the orphaning, and the aggregate fires.
	ws = Lint([]Change{removedSts}, nil, notPruning)
	if _, w := countRule(ws, "removed-statefulset"); !strings.Contains(w.Detail, "orphaned in-cluster") {
		t.Errorf("non-pruning parent: caution should state the orphaning, got %q", w.Detail)
	}
	if n, w := countRule(ws, "not-pruned"); n != 1 || !strings.Contains(w.Detail, "1 removed resource under") {
		t.Errorf("non-pruning parent: want 1 not-pruned aggregate, got %d (%q)", n, w.Detail)
	}

	// Unknown parent — generic wording, no flux suffix, no aggregate.
	ws = Lint([]Change{removedSts}, nil, nil)
	if _, w := countRule(ws, "removed-statefulset"); strings.Contains(w.Detail, "Kustomization") {
		t.Errorf("unknown parent must keep the generic wording, got %q", w.Detail)
	}
	if hasRule(ws, "not-pruned") {
		t.Error("not-pruned fired without parent facts")
	}

	// HelmRelease parent: Helm deletes on upgrade either way — no suffix, no
	// aggregate (the orphan semantics are a Kustomization-prune concept).
	hrParent := map[string]ParentInfo{"HelmRelease default/db": {Found: true}}
	removedSts.Parent = "HelmRelease default/db"
	ws = Lint([]Change{removedSts}, nil, hrParent)
	if _, w := countRule(ws, "removed-statefulset"); strings.Contains(w.Detail, "prune") {
		t.Errorf("HelmRelease parent must not get prune wording, got %q", w.Detail)
	}
	if hasRule(ws, "not-pruned") {
		t.Error("not-pruned fired for a HelmRelease parent")
	}
}

// The aggregate counts only removals — changed/added resources under a
// non-pruning Kustomization are reconciled normally.
func TestFlux_NotPrunedCountsOnlyRemovals(t *testing.T) {
	t.Parallel()
	parents := map[string]ParentInfo{
		"Kustomization flux-system/apps": {Found: true, IsKustomization: true, Prune: false},
	}
	changes := []Change{
		{Status: "changed", Kind: "Deployment", Name: "a", Parent: "Kustomization flux-system/apps", Old: map[string]any{}, New: map[string]any{}},
		{Status: statusRemoved, Kind: "ConfigMap", Name: "b", Parent: "Kustomization flux-system/apps", Old: map[string]any{}},
		{Status: statusRemoved, Kind: "Service", Name: "c", Parent: "Kustomization flux-system/apps", Old: map[string]any{}},
	}
	n, w := countRule(Lint(changes, nil, parents), "not-pruned")
	if n != 1 || !strings.Contains(w.Detail, "2 removed resources") {
		t.Errorf("want one aggregate counting the 2 removals, got %d (%q)", n, w.Detail)
	}
}

// Belt and braces: the warnings keep the api caution level (the UI keys
// styling off it).
func TestFlux_WarningsAreCautions(t *testing.T) {
	t.Parallel()
	c := Change{
		Status: "changed", Kind: "Kustomization", Name: "x",
		Old: fluxDoc("Kustomization", map[string]any{}),
		New: fluxDoc("Kustomization", map[string]any{"suspend": true}),
	}
	for _, w := range Lint([]Change{c}, nil, nil) {
		if w.Level != api.LevelCaution {
			t.Errorf("warning %q level = %q, want caution", w.Rule, w.Level)
		}
	}
}
