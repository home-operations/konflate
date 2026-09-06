package diff

import (
	"fmt"
	"path"
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

// Change.Status values — the api status union. ("changed" is the default arm
// everywhere, so it has no named constant.)
const (
	statusAdded   = "added"
	statusRemoved = "removed"
)

const (
	kindHelmRelease    = "HelmRelease"
	ruleMajorChartBump = "major-chart-bump"
	spec               = "spec"
)

// Lint runs the diff-lint rules over the changed resources (and the diff's image
// changes) and returns the warnings in rule order. parents carries the
// Flux-semantic facts about each producing Kustomization/HelmRelease
// (suspend/prune — see ParentInfo); nil is fine and simply mutes the
// parent-aware rules. Every rule is api.LevelCaution today (advisory → a neutral
// check); the summary groups warnings by tier (see api.WarningsByLevel).
func Lint(changes []Change, images []api.ImageChange, parents map[string]ParentInfo) []api.Warning {
	var warnings []api.Warning
	add := func(rule, detail string, c Change) {
		warnings = append(warnings, api.Warning{Level: api.LevelCaution, Rule: rule, Resource: resourceLabel(c), Detail: detail})
	}

	for _, c := range changes {
		if c.Status == statusRemoved {
			// pruneSuffix sharpens each removal with what Flux will actually
			// do — prune (a real deletion) vs orphan — when the parent
			// Kustomization's spec is known.
			switch c.Kind {
			case kindStatefulSet:
				add("removed-statefulset", "removed StatefulSet; its PersistentVolumeClaims and data may be deleted"+pruneSuffix(c, parents), c)
			case "PersistentVolumeClaim":
				add("removed-pvc", "removed PersistentVolumeClaim; the bound volume's data may be reclaimed"+pruneSuffix(c, parents), c)
			case "Namespace":
				add("removed-namespace", "removed Namespace; deletes every resource inside it"+pruneSuffix(c, parents), c)
			case "CustomResourceDefinition":
				add("removed-crd", "removed CustomResourceDefinition; deletes all of its custom resources"+pruneSuffix(c, parents), c)
			case "NetworkPolicy":
				add("removed-networkpolicy", "removed NetworkPolicy; traffic it previously denied may now be allowed"+pruneSuffix(c, parents), c)
			}
		}

		// Flux suspend toggles on the Kustomization/HelmRelease doc itself.
		warnings = append(warnings, suspendToggleWarnings(c)...)

		// Post-image rules (added or changed): inspect the New manifest.
		if c.New != nil {
			if hasPrivilegedContainer(c.New) {
				add("privileged", "a container runs with securityContext.privileged: true", c)
			}
			if isWorkload(c.Kind) {
				if r, ok := intField(c.New, "spec", "replicas"); ok && r == 0 {
					add("replicas-zero", "spec.replicas is 0; the workload will be scaled to no pods", c)
				}
			}
		}

		if c.Status == statusAdded && c.Kind == "ClusterRoleBinding" {
			add("rbac-widened", "new ClusterRoleBinding; grants cluster-wide permissions", c)
		}

		// Immutable-field rules: a changed resource whose diff touches a field
		// the API server refuses to update in place (see immutable.go).
		warnings = append(warnings, immutableFieldWarnings(c)...)
	}

	// Flux-semantic aggregates: changes parked under a suspended parent, and
	// removals a non-pruning Kustomization will orphan rather than delete.
	warnings = append(warnings, suspendedParentWarnings(changes, parents)...)
	warnings = append(warnings, notPrunedWarnings(changes, parents)...)

	// Blast-radius / version signals: an unusually large change set, and major
	// (semver) chart, source, or container-image bumps.
	if w, ok := largeChangeSet(changes); ok {
		warnings = append(warnings, w)
	}
	refBumps := chartRefBumps(changes)
	for _, b := range refBumps {
		warnings = append(warnings, b.warning())
	}
	warnings = append(warnings, chartBumpWarnings(changes, refBumps)...)
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
		Detail:   "large change set; more ground to cover than a typical PR; review with extra care",
	}, true
}

// Detail formats for a major chart / source bump; %s is the "from → to"
// versions, optionally followed by "across N <objects>" for a grouped bump.
const (
	chartBumpDetail  = "major chart version bump %s; check the chart's upgrade notes for breaking changes"
	sourceBumpDetail = "major version bump %s of the OCI source; check the upstream release notes for breaking changes"
)

// refBump is a major bump of the version Flux source objects pin, found by
// chartRefBumps; warning renders it as one caution. resources lists every
// object that pins the same bump (the first is the one the caution names), so a
// grouped Renovate PR bumping one chart across many HelmReleases reads as one
// finding, not one per HelmRelease.
type refBump struct {
	resources []string // "Kind ns/name" of each HelmRelease / OCIRepository
	kind      string
	rule      string
	chart     string // chart name, to pair with the label rule's; "" when unknown
	from, to  string
}

func (b refBump) warning() api.Warning {
	versions := b.from + " → " + b.to
	if n := len(b.resources); n > 1 {
		versions += fmt.Sprintf(" across %d %ss", n, b.kind)
	}
	detail := sourceBumpDetail
	if b.rule == ruleMajorChartBump {
		detail = chartBumpDetail
	}
	return api.Warning{
		Level:    api.LevelCaution,
		Rule:     b.rule,
		Resource: b.resources[0],
		Detail:   fmt.Sprintf(detail, versions),
	}
}

// chartRefBumps finds a major bump of the chart version a Flux object pins — a
// HelmRelease's spec.chart.spec.version, or the tag an OCIRepository tracks —
// read from the object's own before/after manifests. The label-based
// chartBumpWarnings only sees a bump through the rendered children's
// helm.sh/chart label, so it is blind when the children didn't change (the
// render didn't pick the new chart up) and when the bump is the source object
// itself. A HelmRelease's version may be a range ("1.x"); majorOf rejects those,
// so only a pinned version can fire. An OCIRepository may track a non-chart
// artifact (a manifests bundle), so its rule is named for the source, not a chart.
// Guarded by API group like fluxKind: a non-Flux CRD that happens to be called
// HelmRelease must not trip it. Objects pinning the same chart through the same
// versions fold into one refBump (first-seen order): HelmReleases by chart name,
// OCIRepositories by full URL, since two registries can both serve a chart called
// "app" and those are different upgrades with different release notes. A bump
// whose identity is unknown stays per-object.
func chartRefBumps(changes []Change) []refBump {
	type key struct{ rule, identity, from, to string }
	var out []refBump
	index := map[key]int{}
	for _, c := range changes {
		if c.Old == nil || c.New == nil || !fluxAPI(c) {
			continue // an add/remove has no before→after to compare
		}
		var fieldPath []string
		var rule, chart, identity string
		switch c.Kind {
		case kindHelmRelease:
			fieldPath, rule = []string{spec, "chart", spec, "version"}, ruleMajorChartBump
			chart, _ = stringField(c.New, spec, "chart", spec, "chart")
			identity = chart
		case "OCIRepository":
			fieldPath, rule = []string{spec, "ref", "tag"}, "major-source-bump"
			// oci://ghcr.io/prometheus-community/charts/kube-prometheus-stack → the
			// last path segment is the chart (repository) name the label rule sees;
			// the whole URL is what makes the source distinct.
			if u, ok := stringField(c.New, spec, "url"); ok {
				identity = u
				chart = path.Base(strings.TrimRight(u, "/"))
			}
		default:
			continue
		}
		from, ok1 := stringField(c.Old, fieldPath...)
		to, ok2 := stringField(c.New, fieldPath...)
		if !ok1 || !ok2 || !isMajorBump(from, to) {
			continue
		}
		label := resourceLabel(c)
		if identity != "" {
			k := key{rule, identity, from, to}
			if i, ok := index[k]; ok {
				out[i].resources = append(out[i].resources, label)
				continue
			}
			index[k] = len(out)
		}
		out = append(out, refBump{resources: []string{label}, kind: c.Kind, rule: rule, chart: chart, from: from, to: to})
	}
	return out
}

// chartBumpWarnings flags major Helm chart version bumps (caution), one per
// chart, in first-seen order. A chart's children share its helm.sh/chart label,
// so the bump is deduped by chart name. A bump chartRefBumps already reported is
// skipped so one chart upgrade doesn't surface twice: by identity when the
// child's producing HelmRelease is one that fired (the pinned string and the
// label can spell the same version differently — "v4.0.0" vs "4.0.0", a chart
// path vs its name), else by the same chart moving through the same versions
// (the OCIRepository case, where the child's parent is the HelmRelease that
// consumes it). An unrelated chart that happens to share the version pair is
// still reported.
func chartBumpWarnings(changes []Change, refBumps []refBump) []api.Warning {
	type bump struct{ from, to string }
	type chartBump struct{ chart, from, to string }
	reported := make(map[chartBump]struct{}, len(refBumps))
	firedParents := map[string]struct{}{}
	for _, b := range refBumps {
		if b.chart != "" {
			reported[chartBump{b.chart, b.from, b.to}] = struct{}{}
		}
		if b.kind == kindHelmRelease {
			for _, r := range b.resources {
				firedParents[r] = struct{}{}
			}
		}
	}
	seen := make(map[string]bump)
	var order []string
	for _, c := range changes {
		oldName, oldVer := splitChart(c.OldChart)
		newName, newVer := splitChart(c.NewChart)
		if oldName == "" || oldName != newName || !isMajorBump(oldVer, newVer) {
			continue
		}
		if _, dup := firedParents[c.Parent]; dup {
			continue
		}
		if _, dup := reported[chartBump{oldName, oldVer, newVer}]; dup {
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
			Rule:     ruleMajorChartBump,
			Resource: name,
			Detail:   fmt.Sprintf(chartBumpDetail, b.from+" → "+b.to),
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
				Detail:   fmt.Sprintf("major image version bump %s → %s; likely breaking changes", api.TagOf(img.From), api.TagOf(img.To)),
			})
		}
	}
	return out
}

// chartLabelRe splits a "helm.sh/chart" label ("<name>-<version>") into name and
// version. The version is the trailing "[v]<major>.<minor>…" run, so a chart
// name that itself contains hyphens (e.g. "cert-manager") stays intact. The class
// includes "_" because Helm renders the label as
// `{{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}`, so a build-metadata
// version ("1.2.3+build" → label "…-1.2.3_build") carries an underscore.
var chartLabelRe = regexp.MustCompile(`^(.*)-(v?\d+\.\d+[0-9A-Za-z._+-]*)$`)

func splitChart(label string) (name, version string) {
	m := chartLabelRe.FindStringSubmatch(label)
	if m == nil {
		return "", ""
	}
	// Undo Helm's "+"→"_" label sanitization so the version is valid semver again
	// (chart names contain no "_", so the only "_" is the build separator) — else
	// majorOf's strict semver parse rejects it and the bump silently never fires.
	return m[1], strings.ReplaceAll(m[2], "_", "+")
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
	ver, err := semver.NewVersion(strings.TrimSpace(api.TagOf(v)))
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
	case "Deployment", kindStatefulSet, "ReplicaSet", "ReplicationController":
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
	for _, base := range [][]string{
		{spec},
		{spec, fieldTemplate, spec},
		{spec, "jobTemplate", spec, fieldTemplate, spec},
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
