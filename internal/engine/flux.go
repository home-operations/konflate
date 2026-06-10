package engine

import (
	"strings"

	"github.com/home-operations/flate/pkg/manifest"

	"github.com/home-operations/konflate/internal/diff"
)

// parentInfos scans the full rendered manifest sets for the Flux Kustomization
// and HelmRelease documents and returns their suspend/prune facts, keyed by the
// same "Kind ns/name" label the diff changes carry as Parent. The head side is
// the post-merge truth and wins; the base side fills in parents the PR removes
// (so a removal's prune semantics still resolve). The docs are scanned from the
// manifest VALUES — a Kustomization/HelmRelease is itself a rendered resource
// of whatever produced it — so the index covers every parent the cluster
// declares, changed by this PR or not.
func parentInfos(base, head map[manifest.NamedResource][]map[string]any) map[string]diff.ParentInfo {
	out := map[string]diff.ParentInfo{}
	// Base first, head second: same key ⇒ the head-side doc overwrites, making
	// the post-merge spec the one the rules see.
	for _, side := range []map[manifest.NamedResource][]map[string]any{base, head} {
		for _, docs := range side {
			for _, doc := range docs {
				kind, _ := doc["kind"].(string)
				if kind != "Kustomization" && kind != "HelmRelease" {
					continue
				}
				apiVersion, _ := doc["apiVersion"].(string)
				if !strings.Contains(apiVersion, "toolkit.fluxcd.io") {
					continue
				}
				meta, _ := doc["metadata"].(map[string]any)
				name, _ := meta["name"].(string)
				if name == "" {
					continue
				}
				ns, _ := meta["namespace"].(string)
				spec, _ := doc["spec"].(map[string]any)
				suspended, _ := spec["suspend"].(bool)
				prune, _ := spec["prune"].(bool)
				out[parentLabel(manifest.NamedResource{Kind: kind, Namespace: ns, Name: name})] = diff.ParentInfo{
					Found:           true,
					IsKustomization: kind == "Kustomization",
					Suspended:       suspended,
					Prune:           prune,
				}
			}
		}
	}
	return out
}
