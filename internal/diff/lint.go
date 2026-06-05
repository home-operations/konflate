package diff

import "github.com/home-operations/konflate/internal/api"

// Change is the minimal view of one changed resource the danger-lint needs:
// its status and identity plus the pre-image (Old) and post-image (New)
// manifests. Old is nil for added resources; New is nil for removed ones.
type Change struct {
	Status    string // api status: "added" | "changed" | "removed"
	Kind      string
	Namespace string
	Name      string
	Parent    string // producing HelmRelease/Kustomization label (for Impact)
	Old       map[string]any
	New       map[string]any
}

// Lint runs the danger-lint rules over the changed resources and returns the
// warnings, most-severe first (all dangers before all cautions). Advisory only
// — konflate never blocks on these; they are a reviewer aid.
func Lint(changes []Change) []api.Warning {
	var danger, caution []api.Warning
	add := func(level, rule, detail string, c Change) {
		w := api.Warning{Level: level, Rule: rule, Resource: resourceLabel(c), Detail: detail}
		if level == api.LevelDanger {
			danger = append(danger, w)
		} else {
			caution = append(caution, w)
		}
	}

	for _, c := range changes {
		if c.Status == "removed" {
			switch c.Kind {
			case "StatefulSet":
				add(api.LevelDanger, "removed-statefulset", "removed StatefulSet — its PersistentVolumeClaims and data may be deleted", c)
			case "PersistentVolumeClaim":
				add(api.LevelDanger, "removed-pvc", "removed PersistentVolumeClaim — the bound volume's data may be reclaimed", c)
			case "Namespace":
				add(api.LevelDanger, "removed-namespace", "removed Namespace — deletes every resource inside it", c)
			case "CustomResourceDefinition":
				add(api.LevelDanger, "removed-crd", "removed CustomResourceDefinition — deletes all of its custom resources", c)
			case "NetworkPolicy":
				add(api.LevelCaution, "removed-networkpolicy", "removed NetworkPolicy — traffic it previously denied may now be allowed", c)
			}
		}

		// Post-image rules (added or changed): inspect the New manifest.
		if c.New != nil {
			if hasPrivilegedContainer(c.New) {
				add(api.LevelDanger, "privileged", "a container runs with securityContext.privileged: true", c)
			}
			if isWorkload(c.Kind) {
				if r, ok := intField(c.New, "spec", "replicas"); ok && r == 0 {
					add(api.LevelCaution, "replicas-zero", "spec.replicas is 0 — the workload will be scaled to no pods", c)
				}
			}
		}

		if c.Status == "added" && c.Kind == "ClusterRoleBinding" {
			add(api.LevelCaution, "rbac-widened", "new ClusterRoleBinding — grants cluster-wide permissions", c)
		}
	}

	return append(danger, caution...)
}

// resourceLabel renders "Kind ns/name", or "Kind name" for cluster-scoped
// resources (empty namespace).
func resourceLabel(c Change) string {
	name := c.Name
	if c.Namespace != "" {
		name = c.Namespace + "/" + c.Name
	}
	return c.Kind + " " + name
}

// isWorkload reports whether kind carries a replica count worth flagging.
func isWorkload(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "ReplicaSet", "ReplicationController":
		return true
	default:
		return false
	}
}

// nestedMap walks m down the given keys, returning the map at the end of the
// path (false if any segment is missing or not a map).
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// intField reads a numeric field at the given path as an int64, tolerating the
// int / int64 / float64 forms that YAML/JSON decoding can produce.
func intField(m map[string]any, keys ...string) (int64, bool) {
	if len(keys) == 0 {
		return 0, false
	}
	parent, ok := nestedMap(m, keys[:len(keys)-1]...)
	if !ok {
		return 0, false
	}
	switch n := parent[keys[len(keys)-1]].(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// hasPrivilegedContainer reports whether any container in the pod spec — at the
// pod location (spec) or the workload template location (spec.template.spec) —
// sets securityContext.privileged: true.
func hasPrivilegedContainer(m map[string]any) bool {
	const spec = "spec"
	for _, base := range [][]string{{spec}, {spec, "template", spec}} {
		podSpec, ok := nestedMap(m, base...)
		if !ok {
			continue
		}
		for _, listKey := range []string{"containers", "initContainers"} {
			list, ok := podSpec[listKey].([]any)
			if !ok {
				continue
			}
			for _, item := range list {
				container, ok := item.(map[string]any)
				if !ok {
					continue
				}
				sc, ok := container["securityContext"].(map[string]any)
				if !ok {
					continue
				}
				if priv, ok := sc["privileged"].(bool); ok && priv {
					return true
				}
			}
		}
	}
	return false
}
