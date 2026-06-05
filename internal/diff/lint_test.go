package diff

import (
	"testing"

	"github.com/home-operations/konflate/internal/api"
)

// privilegedDeploymentManifest is a workload post-image with one container
// running privileged, nested at spec.template.spec.containers (where a
// Deployment/StatefulSet/DaemonSet carries its pod template).
func privilegedDeploymentManifest() map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":            "app",
							"securityContext": map[string]any{"privileged": true},
						},
					},
				},
			},
		},
	}
}

func TestLint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes []Change
		want    []api.Warning // compared on Level/Rule/Resource, in order; Detail must be non-empty
	}{
		{
			name:    "removed StatefulSet is a data-loss danger",
			changes: []Change{{Status: "removed", Kind: "StatefulSet", Namespace: "db", Name: "postgres", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelDanger, Rule: "removed-statefulset", Resource: "StatefulSet db/postgres"}},
		},
		{
			name:    "removed PVC is a data-loss danger",
			changes: []Change{{Status: "removed", Kind: "PersistentVolumeClaim", Namespace: "db", Name: "data", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelDanger, Rule: "removed-pvc", Resource: "PersistentVolumeClaim db/data"}},
		},
		{
			name:    "removed Namespace is a danger; cluster-scoped so no ns in the label",
			changes: []Change{{Status: "removed", Kind: "Namespace", Name: "prod", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelDanger, Rule: "removed-namespace", Resource: "Namespace prod"}},
		},
		{
			name:    "removed CRD is a danger",
			changes: []Change{{Status: "removed", Kind: "CustomResourceDefinition", Name: "certificates.cert-manager.io", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelDanger, Rule: "removed-crd", Resource: "CustomResourceDefinition certificates.cert-manager.io"}},
		},
		{
			name:    "removed NetworkPolicy is a caution (traffic may now be allowed)",
			changes: []Change{{Status: "removed", Kind: "NetworkPolicy", Namespace: "web", Name: "default-deny", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "removed-networkpolicy", Resource: "NetworkPolicy web/default-deny"}},
		},
		{
			name: "deployment scaled to zero is a caution",
			changes: []Change{{
				Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api",
				Old: map[string]any{"spec": map[string]any{"replicas": 3}},
				New: map[string]any{"spec": map[string]any{"replicas": 0}},
			}},
			want: []api.Warning{{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api"}},
		},
		{
			name: "a privileged container is a danger",
			changes: []Change{{
				Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api",
				New: privilegedDeploymentManifest(),
			}},
			want: []api.Warning{{Level: api.LevelDanger, Rule: "privileged", Resource: "Deployment web/api"}},
		},
		{
			name:    "added ClusterRoleBinding widens RBAC (caution)",
			changes: []Change{{Status: "added", Kind: "ClusterRoleBinding", Name: "app-admin", New: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "rbac-widened", Resource: "ClusterRoleBinding app-admin"}},
		},
		{
			name: "a benign changed ConfigMap produces no warnings",
			changes: []Change{{
				Status: "changed", Kind: "ConfigMap", Namespace: "web", Name: "cfg",
				Old: map[string]any{"data": map[string]any{"a": "1"}},
				New: map[string]any{"data": map[string]any{"a": "2"}},
			}},
			want: nil,
		},
		{
			name: "a Deployment with replicas 3 (not zero) is clean",
			changes: []Change{{
				Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api",
				New: map[string]any{"spec": map[string]any{"replicas": 3}},
			}},
			want: nil,
		},
		{
			name: "dangers are ordered before cautions regardless of input order",
			changes: []Change{
				{Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api",
					New: map[string]any{"spec": map[string]any{"replicas": 0}}}, // caution
				{Status: "removed", Kind: "StatefulSet", Namespace: "db", Name: "postgres", Old: map[string]any{}}, // danger
			},
			want: []api.Warning{
				{Level: api.LevelDanger, Rule: "removed-statefulset", Resource: "StatefulSet db/postgres"},
				{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Lint(tt.changes)

			if len(got) != len(tt.want) {
				t.Fatalf("Lint() = %d warnings, want %d\n got: %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range tt.want {
				if got[i].Level != tt.want[i].Level || got[i].Rule != tt.want[i].Rule || got[i].Resource != tt.want[i].Resource {
					t.Errorf("warning[%d] = {%s %s %q}, want {%s %s %q}",
						i, got[i].Level, got[i].Rule, got[i].Resource,
						tt.want[i].Level, tt.want[i].Rule, tt.want[i].Resource)
				}
				if got[i].Detail == "" {
					t.Errorf("warning[%d] (%s) has empty Detail; every warning must explain what/why", i, got[i].Rule)
				}
			}
		})
	}
}
