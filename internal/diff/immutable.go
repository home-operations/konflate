package diff

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/home-operations/konflate/internal/api"
)

// Immutable-field lint: a changed resource whose diff touches a field the API
// server refuses to update looks like any other "~ changed" hunk in review,
// but the apply will be rejected ("field is immutable" / "Forbidden: updates
// to statefulset spec for fields other than …"), wedging the Flux
// reconciliation until the object is recreated (or force is enabled on the
// Kustomization/HelmRelease). These rules surface that before merge.
//
// The rule set is deliberately the short list of immutability wedges that
// actually occur in GitOps repos — not a schema-complete validator:
//
//   - StatefulSet: everything in spec outside the API's mutable allowlist
//     (replicas, ordinals, template, updateStrategy,
//     persistentVolumeClaimRetentionPolicy, minReadySeconds). Catches the
//     classic volumeClaimTemplates storage bump.
//   - Deployment / DaemonSet / ReplicaSet: spec.selector.
//   - Job: spec.selector and spec.template.
//   - PersistentVolumeClaim: every spec field except the expand-only
//     spec.resources and spec.volumeAttributesClassName — plus a dedicated
//     rule for a storage request *decrease* (PVCs can only grow).
//   - RoleBinding / ClusterRoleBinding: roleRef.
//   - Service: spec.clusterIP, when both sides pin one (an unset side means
//     the server allocated it, which never appears in a rendered diff).
//
// Kind / field names shared across the lint rules (goconst).
const (
	kindStatefulSet = "StatefulSet"
	fieldTemplate   = "template"
)

func immutableFieldWarnings(c Change) []api.Warning {
	// Only a changed resource can trip an immutable-field update; adds and
	// removes never in-place-update anything.
	if c.Old == nil || c.New == nil {
		return nil
	}

	var fields []string
	var out []api.Warning

	switch c.Kind {
	case kindStatefulSet:
		fields = changedSpecKeysOutside(c, map[string]bool{
			"replicas": true, "ordinals": true, fieldTemplate: true,
			"updateStrategy": true, "persistentVolumeClaimRetentionPolicy": true,
			"minReadySeconds": true,
		})
	case "Deployment", "DaemonSet", "ReplicaSet":
		fields = changedFields(c, "selector")
	case "Job":
		fields = changedFields(c, "selector", fieldTemplate)
	case "PersistentVolumeClaim":
		fields = changedSpecKeysOutside(c, map[string]bool{
			"resources": true, "volumeAttributesClassName": true,
		})
		if w, ok := pvcShrinkWarning(c); ok {
			out = append(out, w)
		}
	case "RoleBinding", "ClusterRoleBinding":
		if !jsonEqual(c.Old["roleRef"], c.New["roleRef"]) {
			fields = []string{"roleRef"}
		}
	case "Service":
		oldIP, _ := stringField(c.Old, "spec", "clusterIP")
		newIP, _ := stringField(c.New, "spec", "clusterIP")
		if oldIP != "" && newIP != "" && oldIP != newIP {
			fields = []string{"spec.clusterIP"}
		}
	}

	if len(fields) > 0 {
		out = append(out, api.Warning{
			Level:    api.LevelCaution,
			Rule:     "immutable-field",
			Resource: resourceLabel(c),
			Detail: fmt.Sprintf("%s changed — immutable on %s; the apply fails until the resource is recreated (or Flux force is enabled)",
				strings.Join(fields, ", "), c.Kind),
		})
	}
	return out
}

// pvcShrinkWarning flags a PersistentVolumeClaim whose storage request got
// smaller: expansion is the one mutation PVCs allow, and only upward — the API
// server rejects a shrink outright. Values that don't parse as quantities are
// ignored (better silent than a false caution on an exotic value).
func pvcShrinkWarning(c Change) (api.Warning, bool) {
	oldRaw, ok1 := stringField(c.Old, "spec", "resources", "requests", "storage")
	newRaw, ok2 := stringField(c.New, "spec", "resources", "requests", "storage")
	if !ok1 || !ok2 || oldRaw == newRaw {
		return api.Warning{}, false
	}
	oldQ, err1 := resource.ParseQuantity(oldRaw)
	newQ, err2 := resource.ParseQuantity(newRaw)
	if err1 != nil || err2 != nil || newQ.Cmp(oldQ) >= 0 {
		return api.Warning{}, false
	}
	return api.Warning{
		Level:    api.LevelCaution,
		Rule:     "pvc-shrink",
		Resource: resourceLabel(c),
		Detail: fmt.Sprintf("storage request decreased %s → %s — PersistentVolumeClaims can only grow; the apply will be rejected",
			oldRaw, newRaw),
	}, true
}

// changedSpecKeysOutside returns "spec.<key>" for every top-level spec key —
// across both sides — whose value differs and is not in the mutable allowlist.
// Sorted, so a multi-field caution reads deterministically.
func changedSpecKeysOutside(c Change, mutable map[string]bool) []string {
	oldSpec, _ := nestedMap(c.Old, "spec")
	newSpec, _ := nestedMap(c.New, "spec")
	keys := make(map[string]struct{}, len(oldSpec)+len(newSpec))
	for k := range oldSpec {
		keys[k] = struct{}{}
	}
	for k := range newSpec {
		keys[k] = struct{}{}
	}
	var out []string
	for k := range keys {
		if mutable[k] || jsonEqual(oldSpec[k], newSpec[k]) {
			continue
		}
		out = append(out, "spec."+k)
	}
	slices.Sort(out)
	return out
}

// changedFields returns "spec.<key>" for each named spec key whose value
// differs between the two sides, in the given order.
func changedFields(c Change, keys ...string) []string {
	oldSpec, _ := nestedMap(c.Old, "spec")
	newSpec, _ := nestedMap(c.New, "spec")
	var out []string
	for _, k := range keys {
		if !jsonEqual(oldSpec[k], newSpec[k]) {
			out = append(out, "spec."+k)
		}
	}
	return out
}

// jsonEqual compares two decoded YAML/JSON values through their canonical JSON
// encoding. reflect.DeepEqual would read int64(3) and float64(3) — the same
// manifest value decoded via different paths — as a difference; the JSON
// round-trip collapses that numeric skew (and map ordering is keyed, so
// marshaling is deterministic).
func jsonEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ab) == string(bb)
}

// stringField reads a string at the given path (false when missing or not a
// string).
func stringField(m map[string]any, keys ...string) (string, bool) {
	if len(keys) == 0 {
		return "", false
	}
	parent, ok := nestedMap(m, keys[:len(keys)-1]...)
	if !ok {
		return "", false
	}
	s, ok := parent[keys[len(keys)-1]].(string)
	return s, ok
}
