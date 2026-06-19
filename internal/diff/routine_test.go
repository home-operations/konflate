package diff

import "testing"

// podSpec wraps containers in the Deployment/StatefulSet pod-template path.
func podSpec(containers ...map[string]any) map[string]any {
	cs := make([]any, len(containers))
	for i, c := range containers {
		cs[i] = c
	}
	return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": cs}}}}
}

func labels(l map[string]any) map[string]any {
	return map[string]any{"metadata": map[string]any{"labels": l}}
}

func TestOnlyImageOrVersionChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes []Change
		want    bool
	}{
		{
			name: "container image only",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "ghcr.io/x:1.0"}),
				New: podSpec(map[string]any{"name": "app", "image": "ghcr.io/x:1.1"})}},
			want: true,
		},
		{
			name: "helm.sh/chart label only",
			changes: []Change{{Status: "changed", Kind: "ServiceMonitor",
				Old: labels(map[string]any{"app.kubernetes.io/name": "x", "helm.sh/chart": "x-0.16.1"}),
				New: labels(map[string]any{"app.kubernetes.io/name": "x", "helm.sh/chart": "x-0.17.0"})}},
			want: true,
		},
		{
			name: "OCIRepository ref tag only",
			changes: []Change{{Status: "changed", Kind: "OCIRepository",
				Old: map[string]any{"spec": map[string]any{"ref": map[string]any{"tag": "0.16.1"}}},
				New: map[string]any{"spec": map[string]any{"ref": map[string]any{"tag": "0.17.0"}}}}},
			want: true,
		},
		{
			name: "chart label + OCI tag together (the common Renovate bump)",
			changes: []Change{
				{Status: "changed", Kind: "ServiceMonitor",
					Old: labels(map[string]any{"helm.sh/chart": "x-0.16.1"}),
					New: labels(map[string]any{"helm.sh/chart": "x-0.17.0"})},
				{Status: "changed", Kind: "OCIRepository",
					Old: map[string]any{"spec": map[string]any{"ref": map[string]any{"tag": "0.16.1"}}},
					New: map[string]any{"spec": map[string]any{"ref": map[string]any{"tag": "0.17.0"}}}},
			},
			want: true,
		},
		{
			name: "app.kubernetes.io/version label",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: labels(map[string]any{"app.kubernetes.io/version": "1.0.0"}),
				New: labels(map[string]any{"app.kubernetes.io/version": "1.1.0"})}},
			want: true,
		},
		{
			name: "HelmRelease chart version",
			changes: []Change{{Status: "changed", Kind: "HelmRelease",
				Old: map[string]any{"spec": map[string]any{"chart": map[string]any{"spec": map[string]any{"version": "1.0.0"}}}},
				New: map[string]any{"spec": map[string]any{"chart": map[string]any{"spec": map[string]any{"version": "1.1.0"}}}}}},
			want: true,
		},
		{
			name:    "no changes",
			changes: nil,
			want:    false,
		},
		{
			name: "env var added is not routine",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "x:1"}),
				New: podSpec(map[string]any{"name": "app", "image": "x:1", "env": []any{map[string]any{"name": "FOO", "value": "bar"}}})}},
			want: false,
		},
		{
			name: "replicas change is not routine",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: map[string]any{"spec": map[string]any{"replicas": float64(1)}},
				New: map[string]any{"spec": map[string]any{"replicas": float64(3)}}}},
			want: false,
		},
		{
			name: "resource-limit change is not routine",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "x:1", "resources": map[string]any{"limits": map[string]any{"memory": "64Mi"}}}),
				New: podSpec(map[string]any{"name": "app", "image": "x:1", "resources": map[string]any{"limits": map[string]any{"memory": "128Mi"}}})}},
			want: false,
		},
		{
			name: "image bump alongside a non-image change is not routine",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "x:1", "args": []any{"--a"}}),
				New: podSpec(map[string]any{"name": "app", "image": "x:2", "args": []any{"--b"}})}},
			want: false,
		},
		{
			name: "added container (count change) is not routine",
			changes: []Change{{Status: "changed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "x:1"}),
				New: podSpec(map[string]any{"name": "app", "image": "x:1"}, map[string]any{"name": "side", "image": "y:1"})}},
			want: false,
		},
		{
			name: "added resource is not routine",
			changes: []Change{{Status: "added", Kind: "Deployment",
				New: podSpec(map[string]any{"name": "app", "image": "x:1"})}},
			want: false,
		},
		{
			name: "removed resource is not routine",
			changes: []Change{{Status: "removed", Kind: "Deployment",
				Old: podSpec(map[string]any{"name": "app", "image": "x:1"})}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := onlyImageOrVersionChanges(tt.changes); got != tt.want {
				t.Errorf("onlyImageOrVersionChanges = %v, want %v", got, tt.want)
			}
		})
	}
}
