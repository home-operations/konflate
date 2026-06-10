package diff

import (
	"strings"
	"testing"

	"github.com/home-operations/konflate/internal/api"
)

// changed builds a "changed" Change for kind with the given old/new spec maps
// (wrapped under "spec" — pass extra top-level fields via top).
func changed(kind string, oldSpec, newSpec map[string]any) Change {
	return Change{
		Status: "changed", Kind: kind, Namespace: "default", Name: "x",
		Old: map[string]any{"spec": oldSpec},
		New: map[string]any{"spec": newSpec},
	}
}

func lintOne(c Change) []api.Warning { return Lint([]Change{c}, nil, nil) }

func TestImmutable_StatefulSetSpecOutsideAllowlist(t *testing.T) {
	t.Parallel()

	// The classic wedge: a volumeClaimTemplates storage bump.
	vct := func(size string) []any {
		return []any{map[string]any{
			"metadata": map[string]any{"name": "data"},
			"spec": map[string]any{
				"resources": map[string]any{"requests": map[string]any{"storage": size}},
			},
		}}
	}
	ws := lintOne(changed("StatefulSet",
		map[string]any{"serviceName": "db", "volumeClaimTemplates": vct("10Gi")},
		map[string]any{"serviceName": "db", "volumeClaimTemplates": vct("20Gi")},
	))
	n, w := countRule(ws, "immutable-field")
	if n != 1 {
		t.Fatalf("immutable-field warnings = %d, want 1: %+v", n, ws)
	}
	if !strings.Contains(w.Detail, "spec.volumeClaimTemplates") || !strings.Contains(w.Detail, "StatefulSet") {
		t.Errorf("detail should name the field and kind, got %q", w.Detail)
	}

	// Several immutable fields at once: one caution, fields sorted.
	ws = lintOne(changed("StatefulSet",
		map[string]any{"serviceName": "a", "podManagementPolicy": "OrderedReady"},
		map[string]any{"serviceName": "b", "podManagementPolicy": "Parallel"},
	))
	if n, w := countRule(ws, "immutable-field"); n != 1 || !strings.HasPrefix(w.Detail, "spec.podManagementPolicy, spec.serviceName changed") {
		t.Errorf("want one caution listing both fields sorted, got %d: %q", n, w.Detail)
	}
}

func TestImmutable_StatefulSetMutableFieldsAreQuiet(t *testing.T) {
	t.Parallel()
	// Everything the API allows updating must not trip the rule.
	ws := lintOne(changed("StatefulSet",
		map[string]any{
			"replicas": 1, "minReadySeconds": 0,
			"template":                             map[string]any{"spec": map[string]any{"x": "a"}},
			"updateStrategy":                       map[string]any{"type": "RollingUpdate"},
			"persistentVolumeClaimRetentionPolicy": map[string]any{"whenDeleted": "Retain"},
			"ordinals":                             map[string]any{"start": 0},
		},
		map[string]any{
			"replicas": 3, "minReadySeconds": 10,
			"template":                             map[string]any{"spec": map[string]any{"x": "b"}},
			"updateStrategy":                       map[string]any{"type": "OnDelete"},
			"persistentVolumeClaimRetentionPolicy": map[string]any{"whenDeleted": "Delete"},
			"ordinals":                             map[string]any{"start": 1},
		},
	))
	if hasRule(ws, "immutable-field") {
		t.Errorf("mutable StatefulSet fields flagged: %+v", ws)
	}
}

func TestImmutable_SelectorOnWorkloads(t *testing.T) {
	t.Parallel()
	sel := func(v string) map[string]any {
		return map[string]any{"matchLabels": map[string]any{"app": v}}
	}
	for _, kind := range []string{"Deployment", "DaemonSet", "ReplicaSet", "Job"} {
		ws := lintOne(changed(kind, map[string]any{"selector": sel("a")}, map[string]any{"selector": sel("b")}))
		if n, w := countRule(ws, "immutable-field"); n != 1 || !strings.Contains(w.Detail, "spec.selector") {
			t.Errorf("%s selector change: want 1 immutable-field naming spec.selector, got %d (%q)", kind, n, w.Detail)
		}
		// A template change on a Deployment is everyday business.
		if kind == "Deployment" {
			ws := lintOne(changed(kind,
				map[string]any{"selector": sel("a"), "template": map[string]any{"spec": map[string]any{"img": "1"}}},
				map[string]any{"selector": sel("a"), "template": map[string]any{"spec": map[string]any{"img": "2"}}},
			))
			if hasRule(ws, "immutable-field") {
				t.Errorf("Deployment template change flagged: %+v", ws)
			}
		}
	}

	// Job templates are immutable, unlike every other workload's.
	ws := lintOne(changed("Job",
		map[string]any{"template": map[string]any{"spec": map[string]any{"img": "1"}}},
		map[string]any{"template": map[string]any{"spec": map[string]any{"img": "2"}}},
	))
	if n, w := countRule(ws, "immutable-field"); n != 1 || !strings.Contains(w.Detail, "spec.template") {
		t.Errorf("Job template change: want immutable-field naming spec.template, got %d (%q)", n, w.Detail)
	}
}

func TestImmutable_NumericTypeSkewIsNotAChange(t *testing.T) {
	t.Parallel()
	// int vs float64 for the same value — two YAML decode paths — must compare
	// equal, not read as an immutable-field change.
	ws := lintOne(changed("Deployment",
		map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"n": int64(3)}}},
		map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"n": float64(3)}}},
	))
	if hasRule(ws, "immutable-field") {
		t.Errorf("numeric type skew flagged as a change: %+v", ws)
	}
}

func TestImmutable_PVCFieldsAndShrink(t *testing.T) {
	t.Parallel()
	pvc := func(class, size string) map[string]any {
		return map[string]any{
			"storageClassName": class,
			"resources":        map[string]any{"requests": map[string]any{"storage": size}},
		}
	}

	// Storage class swap: immutable.
	ws := lintOne(changed("PersistentVolumeClaim", pvc("fast", "10Gi"), pvc("slow", "10Gi")))
	if n, w := countRule(ws, "immutable-field"); n != 1 || !strings.Contains(w.Detail, "spec.storageClassName") {
		t.Errorf("storageClassName change: want immutable-field, got %d (%q)", n, w.Detail)
	}

	// Expansion is the allowed mutation: no caution either way.
	ws = lintOne(changed("PersistentVolumeClaim", pvc("fast", "10Gi"), pvc("fast", "20Gi")))
	if hasRule(ws, "immutable-field") || hasRule(ws, "pvc-shrink") {
		t.Errorf("PVC expansion flagged: %+v", ws)
	}

	// A shrink is rejected by the API server.
	ws = lintOne(changed("PersistentVolumeClaim", pvc("fast", "20Gi"), pvc("fast", "10Gi")))
	if n, w := countRule(ws, "pvc-shrink"); n != 1 || !strings.Contains(w.Detail, "20Gi → 10Gi") {
		t.Errorf("PVC shrink: want pvc-shrink with values, got %d (%q)", n, w.Detail)
	}

	// Unit-aware: 1Gi → 1024Mi is the same size, not a shrink (and not growth).
	ws = lintOne(changed("PersistentVolumeClaim", pvc("fast", "1Gi"), pvc("fast", "1024Mi")))
	if hasRule(ws, "pvc-shrink") {
		t.Errorf("equal quantities in different units flagged as shrink: %+v", ws)
	}

	// Unparseable quantities stay silent rather than guessing.
	ws = lintOne(changed("PersistentVolumeClaim", pvc("fast", "lots"), pvc("fast", "few")))
	if hasRule(ws, "pvc-shrink") {
		t.Errorf("unparseable quantities flagged: %+v", ws)
	}
}

func TestImmutable_RoleRefAndClusterIP(t *testing.T) {
	t.Parallel()
	roleRef := func(name string) map[string]any {
		return map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name}
	}
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		c := Change{
			Status: "changed", Kind: kind, Name: "x",
			Old: map[string]any{"roleRef": roleRef("view"), "subjects": []any{"a"}},
			New: map[string]any{"roleRef": roleRef("edit"), "subjects": []any{"a"}},
		}
		if n, w := countRule(lintOne(c), "immutable-field"); n != 1 || !strings.Contains(w.Detail, "roleRef") {
			t.Errorf("%s roleRef change: want immutable-field, got %d (%q)", kind, n, w.Detail)
		}
		// Subjects are mutable.
		c.New = map[string]any{"roleRef": roleRef("view"), "subjects": []any{"b"}}
		if hasRule(lintOne(c), "immutable-field") {
			t.Errorf("%s subjects change flagged", kind)
		}
	}

	// Service clusterIP: flagged only when both sides pin one.
	ws := lintOne(changed("Service",
		map[string]any{"clusterIP": "10.0.0.1"}, map[string]any{"clusterIP": "10.0.0.2"}))
	if n, w := countRule(ws, "immutable-field"); n != 1 || !strings.Contains(w.Detail, "spec.clusterIP") {
		t.Errorf("clusterIP change: want immutable-field, got %d (%q)", n, w.Detail)
	}
	ws = lintOne(changed("Service", map[string]any{}, map[string]any{"clusterIP": "10.0.0.2"}))
	if hasRule(ws, "immutable-field") {
		t.Errorf("newly-pinned clusterIP (old side server-allocated) flagged: %+v", ws)
	}
}

func TestImmutable_AddsAndRemovesAreQuiet(t *testing.T) {
	t.Parallel()
	// Adds and removes never in-place-update anything — no immutable warnings,
	// whatever the manifests contain.
	spec := map[string]any{"spec": map[string]any{"serviceName": "db"}}
	for _, c := range []Change{
		{Status: "added", Kind: "StatefulSet", Name: "x", New: spec},
		{Status: "removed", Kind: "StatefulSet", Name: "x", Old: spec},
	} {
		if hasRule(lintOne(c), "immutable-field") {
			t.Errorf("%s resource produced an immutable-field warning", c.Status)
		}
	}
}
