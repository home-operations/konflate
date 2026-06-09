package diff

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/home-operations/konflate/internal/api"
)

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
	// OldChart / NewChart are the resource's "helm.sh/chart" label
	// ("<name>-<version>") before and after, captured before normalize strips it
	// — used to flag a major chart-version bump. Empty for non-Helm resources or
	// the absent side of an add/remove.
	OldChart string
	NewChart string
}

// Lint runs the danger-lint rules over the changed resources (and the diff's
// image changes) and returns the warnings, most-severe first (all dangers
// before all cautions). Advisory only — konflate never blocks on these; they
// are a reviewer aid.
func Lint(changes []Change, images []api.ImageChange) []api.Warning {
	var warnings []api.Warning
	add := func(rule, detail string, c Change) {
		warnings = append(warnings, api.Warning{Level: api.LevelCaution, Rule: rule, Resource: resourceLabel(c), Detail: detail})
	}

	for _, c := range changes {
		if c.Status == "removed" {
			switch c.Kind {
			case "StatefulSet":
				add("removed-statefulset", "removed StatefulSet — its PersistentVolumeClaims and data may be deleted", c)
			case "PersistentVolumeClaim":
				add("removed-pvc", "removed PersistentVolumeClaim — the bound volume's data may be reclaimed", c)
			case "Namespace":
				add("removed-namespace", "removed Namespace — deletes every resource inside it", c)
			case "CustomResourceDefinition":
				add("removed-crd", "removed CustomResourceDefinition — deletes all of its custom resources", c)
			case "NetworkPolicy":
				add("removed-networkpolicy", "removed NetworkPolicy — traffic it previously denied may now be allowed", c)
			}
		}

		// Post-image rules (added or changed): inspect the New manifest.
		if c.New != nil {
			if hasPrivilegedContainer(c.New) {
				add("privileged", "a container runs with securityContext.privileged: true", c)
			}
			if isWorkload(c.Kind) {
				if r, ok := intField(c.New, "spec", "replicas"); ok && r == 0 {
					add("replicas-zero", "spec.replicas is 0 — the workload will be scaled to no pods", c)
				}
			}
		}

		if c.Status == "added" && c.Kind == "ClusterRoleBinding" {
			add("rbac-widened", "new ClusterRoleBinding — grants cluster-wide permissions", c)
		}
	}

	// Blast-radius / version signals: an unusually large change set, and major
	// (semver) chart or container-image bumps.
	if w, ok := largeChangeSet(changes); ok {
		warnings = append(warnings, w)
	}
	warnings = append(warnings, chartBumpWarnings(changes)...)
	warnings = append(warnings, imageBumpWarnings(images)...)

	return warnings
}

// A change set this wide warrants a careful pass — a Renovate-style "update
// everything" PR, or a sweeping refactor. Tuned to skip ordinary PRs (a handful
// of resources in one or two apps) while catching bulk updates. Either bound
// trips it: many apps, or a very large render delta in few.
const (
	largeParentCount   = 10 // distinct HelmReleases/Kustomizations touched
	largeResourceCount = 60 // total changed resources
)

// largeChangeSet flags an unusually broad change set (caution).
func largeChangeSet(changes []Change) (api.Warning, bool) {
	parents := make(map[string]struct{})
	for _, c := range changes {
		if c.Parent != "" {
			parents[c.Parent] = struct{}{}
		}
	}
	n, p := len(changes), len(parents)
	if p < largeParentCount && n < largeResourceCount {
		return api.Warning{}, false
	}
	return api.Warning{
		Level:    api.LevelCaution,
		Rule:     "large-changeset",
		Resource: fmt.Sprintf("%d resources · %d apps", n, p),
		Detail:   "large change set — more ground to cover than a typical PR; review with extra care",
	}, true
}

// chartBumpWarnings flags major Helm chart version bumps (caution), one per
// chart, in first-seen order. A chart's children share its helm.sh/chart label,
// so the bump is deduped by chart name.
func chartBumpWarnings(changes []Change) []api.Warning {
	type bump struct{ from, to string }
	seen := make(map[string]bump)
	var order []string
	for _, c := range changes {
		oldName, oldVer := splitChart(c.OldChart)
		newName, newVer := splitChart(c.NewChart)
		if oldName == "" || oldName != newName || !isMajorBump(oldVer, newVer) {
			continue
		}
		if _, ok := seen[oldName]; !ok {
			seen[oldName] = bump{oldVer, newVer}
			order = append(order, oldName)
		}
	}
	out := make([]api.Warning, 0, len(order))
	for _, name := range order {
		b := seen[name]
		out = append(out, api.Warning{
			Level:    api.LevelCaution,
			Rule:     "major-chart-bump",
			Resource: name,
			Detail: fmt.Sprintf("major chart version bump %s → %s — check the chart's upgrade notes for breaking changes",
				b.from, b.to),
		})
	}
	return out
}

// imageBumpWarnings flags major container-image version bumps (caution). Images
// are already deduped and sorted by the engine.
func imageBumpWarnings(images []api.ImageChange) []api.Warning {
	var out []api.Warning
	for _, img := range images {
		if isMajorBump(img.From, img.To) {
			out = append(out, api.Warning{
				Level:    api.LevelCaution,
				Rule:     "major-image-bump",
				Resource: img.Name,
				Detail:   fmt.Sprintf("major image version bump %s → %s — likely breaking changes", img.From, img.To),
			})
		}
	}
	return out
}

// chartLabelRe splits a "helm.sh/chart" label ("<name>-<version>") into name and
// version. The version is the trailing "[v]<major>.<minor>…" run, so a chart
// name that itself contains hyphens (e.g. "cert-manager") stays intact.
var chartLabelRe = regexp.MustCompile(`^(.*)-(v?\d+\.\d+[0-9A-Za-z.+-]*)$`)

func splitChart(label string) (name, version string) {
	m := chartLabelRe.FindStringSubmatch(label)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// isMajorBump reports whether two semver-ish versions differ in their major
// component. Non-semver values (digests, "latest", date tags) yield no bump.
func isMajorBump(from, to string) bool {
	fm, ok1 := majorOf(from)
	tm, ok2 := majorOf(to)
	return ok1 && ok2 && fm != tm
}

// majorOf parses a tag/version's semver major, tolerating the shapes container
// images and Helm charts use (a leading "v", partials like "1.2", and
// prerelease/build metadata). It returns false for non-semver values (digests,
// "latest", name-only tags) and for an implausibly large major — a calendar or
// date tag like 20240131, or a calver year — so those never read as a major
// bump. (A bare integer such as a postgres:16 tag does parse, by design.)
func majorOf(v string) (uint64, bool) {
	ver, err := semver.NewVersion(strings.TrimSpace(v))
	if err != nil || ver.Major() >= 1000 {
		return 0, false
	}
	return ver.Major(), true
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

// hasPrivilegedContainer reports whether any container in the pod spec sets
// securityContext.privileged: true, at any of the locations a pod template
// appears: the bare Pod (spec), the workload template most kinds use
// (spec.template.spec), and the CronJob's nested job template
// (spec.jobTemplate.spec.template.spec). Without the last, a privileged
// container in a CronJob would slip past the caution that an identical one in a
// Deployment raises.
func hasPrivilegedContainer(m map[string]any) bool {
	const spec = "spec"
	for _, base := range [][]string{
		{spec},
		{spec, "template", spec},
		{spec, "jobTemplate", spec, "template", spec},
	} {
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
