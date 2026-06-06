package diff

import (
	"fmt"
	"testing"

	"github.com/home-operations/konflate/internal/api"
)

func hasRule(ws []api.Warning, rule string) bool {
	for _, w := range ws {
		if w.Rule == rule {
			return true
		}
	}
	return false
}

func countRule(ws []api.Warning, rule string) (int, api.Warning) {
	var n int
	var last api.Warning
	for _, w := range ws {
		if w.Rule == rule {
			n++
			last = w
		}
	}
	return n, last
}

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
			got := Lint(tt.changes, nil)

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

func TestLint_LargeChangeSet(t *testing.T) {
	t.Parallel()
	// Many distinct apps (parents) trips the caution.
	many := make([]Change, 0, largeParentCount)
	for i := range largeParentCount {
		many = append(many, Change{
			Status: "changed", Kind: "ConfigMap", Name: fmt.Sprintf("c%d", i),
			Parent: fmt.Sprintf("HelmRelease app%d", i),
			Old:    map[string]any{}, New: map[string]any{},
		})
	}
	if !hasRule(Lint(many, nil), "large-changeset") {
		t.Errorf("%d apps should trip the large-changeset caution", largeParentCount)
	}
	// A one-app change does not.
	few := []Change{{Status: "changed", Kind: "ConfigMap", Name: "c", Parent: "HelmRelease app", Old: map[string]any{}, New: map[string]any{}}}
	if hasRule(Lint(few, nil), "large-changeset") {
		t.Error("a one-app change must not be flagged as large")
	}
}

func TestLint_MajorImageBump(t *testing.T) {
	t.Parallel()
	images := []api.ImageChange{
		{Name: "ghcr.io/app", From: "v1.9.0", To: "v2.0.0"}, // major → caution
		{Name: "ghcr.io/lib", From: "1.2.3", To: "1.3.0"},   // minor → none
	}
	n, w := countRule(Lint(nil, images), "major-image-bump")
	if n != 1 {
		t.Fatalf("major-image-bump count = %d, want 1", n)
	}
	if w.Level != api.LevelCaution || w.Resource != "ghcr.io/app" || w.Detail == "" {
		t.Errorf("major-image-bump = {%s %q %q}, want caution ghcr.io/app with a detail", w.Level, w.Resource, w.Detail)
	}
}

func TestLint_MajorChartBump(t *testing.T) {
	t.Parallel()
	changes := []Change{
		// Two children of one chart, major 3→4 → a single (deduped) caution.
		{Status: "changed", Kind: "Deployment", Name: "a", Parent: "HelmRelease app", OldChart: "app-template-3.5.1", NewChart: "app-template-4.0.0", Old: map[string]any{}, New: map[string]any{}},
		{Status: "changed", Kind: "Service", Name: "b", Parent: "HelmRelease app", OldChart: "app-template-3.5.1", NewChart: "app-template-4.0.0", Old: map[string]any{}, New: map[string]any{}},
		// A minor chart bump → none.
		{Status: "changed", Kind: "ConfigMap", Name: "c", Parent: "HelmRelease other", OldChart: "other-1.2.0", NewChart: "other-1.3.0", Old: map[string]any{}, New: map[string]any{}},
	}
	n, w := countRule(Lint(changes, nil), "major-chart-bump")
	if n != 1 {
		t.Fatalf("major-chart-bump count = %d, want 1 (deduped by chart)", n)
	}
	if w.Level != api.LevelCaution || w.Resource != "app-template" || w.Detail == "" {
		t.Errorf("major-chart-bump = {%s %q %q}, want caution app-template with a detail", w.Level, w.Resource, w.Detail)
	}
}

func TestMajorOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"1.15.0", 1, true},
		{"v2.0.0", 2, true},
		{"1.2.3-rc1", 1, true},
		{"latest", 0, false},
		{"sha256:deadbeef", 0, false},
		{"20240131", 0, false},   // date tag (no dot)
		{"2024.01.31", 0, false}, // calver (major too large)
		{"3", 0, false},          // bare major (no minor)
		{"", 0, false},
	}
	for _, c := range cases {
		if got, ok := majorOf(c.in); ok != c.ok || (ok && got != c.want) {
			t.Errorf("majorOf(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIsMajorBump(t *testing.T) {
	t.Parallel()
	bump := [][2]string{{"v1.0.0", "v2.0.0"}, {"1.9.9", "2.0.0"}, {"2.0.0", "1.0.0"}}
	noBump := [][2]string{{"1.2.0", "1.3.0"}, {"v1.0.0", "v1.0.1"}, {"latest", "1.0.0"}, {"1.0.0", "sha256:x"}, {"1.0.0", "1.0.0"}}
	for _, p := range bump {
		if !isMajorBump(p[0], p[1]) {
			t.Errorf("isMajorBump(%q, %q) = false, want true", p[0], p[1])
		}
	}
	for _, p := range noBump {
		if isMajorBump(p[0], p[1]) {
			t.Errorf("isMajorBump(%q, %q) = true, want false", p[0], p[1])
		}
	}
}
