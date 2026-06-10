package engine

import (
	"testing"

	"github.com/home-operations/flate/pkg/manifest"
)

// TestParentInfos covers the Flux parent index the lint rules consume: docs
// are found across both manifest sets, the head side wins for parents present
// on both (the post-merge truth), the base side fills in removed parents, and
// non-Flux lookalikes are excluded.
func TestParentInfos(t *testing.T) {
	t.Parallel()
	ks := func(name string, suspend, prune bool, apiVersion string) map[string]any {
		return map[string]any{
			"apiVersion": apiVersion,
			"kind":       "Kustomization",
			"metadata":   map[string]any{"name": name, "namespace": "flux-system"},
			"spec":       map[string]any{"suspend": suspend, "prune": prune},
		}
	}
	hr := func(name string, suspend bool) map[string]any {
		return map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"name": name, "namespace": "media"},
			"spec":       map[string]any{"suspend": suspend},
		}
	}
	parent := manifest.NamedResource{Kind: "Kustomization", Namespace: "flux-system", Name: "root"}

	base := map[manifest.NamedResource][]map[string]any{
		parent: {
			ks("apps", false, true, "kustomize.toolkit.fluxcd.io/v1"),  // suspend flips on head
			ks("gone", false, false, "kustomize.toolkit.fluxcd.io/v1"), // removed by the PR: base fills in
			hr("plex", false),
		},
	}
	head := map[manifest.NamedResource][]map[string]any{
		parent: {
			ks("apps", true, true, "kustomize.toolkit.fluxcd.io/v1"), // head wins: suspended post-merge
			hr("plex", false),
			ks("argo-lookalike", true, true, "example.io/v1"), // wrong API group: excluded
		},
	}

	got := parentInfos(base, head)

	apps := got["Kustomization flux-system/apps"]
	if !apps.Found || !apps.IsKustomization || !apps.Suspended || !apps.Prune {
		t.Errorf("apps: head side (suspended, pruning) should win, got %+v", apps)
	}
	gone := got["Kustomization flux-system/gone"]
	if !gone.Found || gone.Prune {
		t.Errorf("gone: removed parent should resolve from base (prune false), got %+v", gone)
	}
	plex := got["HelmRelease media/plex"]
	if !plex.Found || plex.IsKustomization || plex.Suspended {
		t.Errorf("plex: HelmRelease facts wrong, got %+v", plex)
	}
	if _, ok := got["Kustomization flux-system/argo-lookalike"]; ok {
		t.Error("non-Flux Kustomization lookalike must be excluded")
	}
}
