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
		{
			// PR 11139: app-template HelmRelease pins the image in values as
			// spec.values…image.tag — a routine digest/tag bump, not the rendered
			// container's image leaf.
			name: "image tag pinned in HelmRelease values",
			changes: []Change{{Status: "changed", Kind: "HelmRelease",
				Old: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"repository": "ghcr.io/x", "tag": "1.0.0@sha256:aaa"}}}},
				New: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"repository": "ghcr.io/x", "tag": "1.0.0@sha256:bbb"}}}}}},
			want: true,
		},
		{
			name: "image digest pinned in values",
			changes: []Change{{Status: "changed", Kind: "HelmRelease",
				Old: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"digest": "sha256:aaa"}}}},
				New: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"digest": "sha256:bbb"}}}}}},
			want: true,
		},
		{
			// PR 11140: a chart re-render leaves a field differing only by Go type
			// (int vs float64) that marshals to identical YAML — invisible in the
			// diff, so it must not defeat routine. The only shown change is the label.
			name: "type-only field difference is ignored (phantom diff)",
			changes: []Change{{Status: "changed", Kind: "ServiceMonitor",
				Old: map[string]any{"metadata": map[string]any{"labels": map[string]any{"helm.sh/chart": "x-0.16.1"}}, "spec": map[string]any{"port": 9633}},
				New: map[string]any{"metadata": map[string]any{"labels": map[string]any{"helm.sh/chart": "x-0.17.0"}}, "spec": map[string]any{"port": float64(9633)}}}},
			want: true,
		},
		{
			// Changing the image *repository* (not just tag/digest) is a source swap,
			// not a routine version bump — negative control on the values rule.
			name: "image repository change in values is not routine",
			changes: []Change{{Status: "changed", Kind: "HelmRelease",
				Old: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"repository": "ghcr.io/x", "tag": "1.0"}}}},
				New: map[string]any{"spec": map[string]any{"values": map[string]any{"image": map[string]any{"repository": "ghcr.io/y", "tag": "1.0"}}}}}},
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
