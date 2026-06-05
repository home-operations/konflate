package diff

import (
	"slices"
	"testing"
)

func TestSummarize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		changes       []Change
		wantResources int
		wantParents   int
		wantNS        []string
		wantCRDs      int
	}{
		{
			name: "counts resources, distinct parents, namespaces, and CRDs",
			changes: []Change{
				{Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api", Parent: "HelmRelease web/api"},
				{Status: "added", Kind: "Service", Namespace: "web", Name: "api-svc", Parent: "HelmRelease web/api"}, // same parent
				{Status: "removed", Kind: "ClusterRole", Name: "foo", Parent: "Kustomization flux/apps"},             // cluster-scoped
				{Status: "added", Kind: "CustomResourceDefinition", Name: "widgets.example.com", Parent: "Kustomization flux/crds"},
			},
			wantResources: 4,
			wantParents:   3, // HelmRelease web/api, Kustomization flux/apps, Kustomization flux/crds
			wantNS:        []string{"web"},
			wantCRDs:      1,
		},
		{
			name: "namespaces are sorted and de-duplicated; empty (cluster-scoped) excluded",
			changes: []Change{
				{Status: "changed", Kind: "Deployment", Namespace: "zoo", Name: "a", Parent: "p"},
				{Status: "changed", Kind: "Deployment", Namespace: "apple", Name: "b", Parent: "p"},
				{Status: "changed", Kind: "Deployment", Namespace: "apple", Name: "c", Parent: "p"},
				{Status: "removed", Kind: "ClusterRole", Name: "d", Parent: "p"}, // ns "" — not counted
				{Status: "changed", Kind: "Deployment", Namespace: "moon", Name: "e", Parent: "p"},
			},
			wantResources: 5,
			wantParents:   1,
			wantNS:        []string{"apple", "moon", "zoo"},
			wantCRDs:      0,
		},
		{
			name:          "no changes yields a zero impact",
			changes:       nil,
			wantResources: 0,
			wantParents:   0,
			wantNS:        nil,
			wantCRDs:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Summarize(tt.changes)

			if got.Resources != tt.wantResources {
				t.Errorf("Resources = %d, want %d", got.Resources, tt.wantResources)
			}
			if got.Parents != tt.wantParents {
				t.Errorf("Parents = %d, want %d", got.Parents, tt.wantParents)
			}
			if got.CRDs != tt.wantCRDs {
				t.Errorf("CRDs = %d, want %d", got.CRDs, tt.wantCRDs)
			}
			if !slices.Equal(got.Namespaces, tt.wantNS) {
				t.Errorf("Namespaces = %v, want %v", got.Namespaces, tt.wantNS)
			}
		})
	}
}
