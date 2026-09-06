package diff

import (
	"fmt"
	"strings"
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

// privilegedCronJobManifest puts a privileged container at the deeper location a
// CronJob nests its pod template: spec.jobTemplate.spec.template.spec.containers.
func privilegedCronJobManifest() map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"jobTemplate": map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":            "backup",
									"securityContext": map[string]any{"privileged": true},
								},
							},
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
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "removed-statefulset", Resource: "StatefulSet db/postgres"}},
		},
		{
			name:    "removed PVC is a data-loss danger",
			changes: []Change{{Status: "removed", Kind: "PersistentVolumeClaim", Namespace: "db", Name: "data", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "removed-pvc", Resource: "PersistentVolumeClaim db/data"}},
		},
		{
			name:    "removed Namespace is a danger; cluster-scoped so no ns in the label",
			changes: []Change{{Status: "removed", Kind: "Namespace", Name: "prod", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "removed-namespace", Resource: "Namespace prod"}},
		},
		{
			name:    "removed CRD is a danger",
			changes: []Change{{Status: "removed", Kind: "CustomResourceDefinition", Name: "certificates.cert-manager.io", Old: map[string]any{}}},
			want:    []api.Warning{{Level: api.LevelCaution, Rule: "removed-crd", Resource: "CustomResourceDefinition certificates.cert-manager.io"}},
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
			want: []api.Warning{{Level: api.LevelCaution, Rule: "privileged", Resource: "Deployment web/api"}},
		},
		{
			name: "a privileged container in a CronJob is flagged (deeper pod-template nesting)",
			changes: []Change{{
				Status: "changed", Kind: "CronJob", Namespace: "ops", Name: "backup",
				New: privilegedCronJobManifest(),
			}},
			want: []api.Warning{{Level: api.LevelCaution, Rule: "privileged", Resource: "CronJob ops/backup"}},
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
			name: "every flag is a single caution severity, emitted in change order",
			changes: []Change{
				{Status: "changed", Kind: "Deployment", Namespace: "web", Name: "api",
					New: map[string]any{"spec": map[string]any{"replicas": 0}}},
				{Status: "removed", Kind: "StatefulSet", Namespace: "db", Name: "postgres", Old: map[string]any{}},
			},
			want: []api.Warning{
				{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api"},
				{Level: api.LevelCaution, Rule: "removed-statefulset", Resource: "StatefulSet db/postgres"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Lint(tt.changes, nil, nil)

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
	if !hasRule(Lint(many, nil, nil), "large-changeset") {
		t.Errorf("%d apps should trip the large-changeset caution", largeParentCount)
	}
	// A one-app change does not.
	few := []Change{{Status: "changed", Kind: "ConfigMap", Name: "c", Parent: "HelmRelease app", Old: map[string]any{}, New: map[string]any{}}}
	if hasRule(Lint(few, nil, nil), "large-changeset") {
		t.Error("a one-app change must not be flagged as large")
	}
}

func TestLint_MajorImageBump(t *testing.T) {
	t.Parallel()
	images := []api.ImageChange{
		{Name: "ghcr.io/app", From: "v1.9.0", To: "v2.0.0"}, // major → caution
		{Name: "ghcr.io/lib", From: "1.2.3", To: "1.3.0"},   // minor → none
	}
	n, w := countRule(Lint(nil, images, nil), "major-image-bump")
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
	n, w := countRule(Lint(changes, nil, nil), "major-chart-bump")
	if n != 1 {
		t.Fatalf("major-chart-bump count = %d, want 1 (deduped by chart)", n)
	}
	if w.Level != api.LevelCaution || w.Resource != "app-template" || w.Detail == "" {
		t.Errorf("major-chart-bump = {%s %q %q}, want caution app-template with a detail", w.Level, w.Resource, w.Detail)
	}
}

// A digest-pinned tag ("v1.9.0@sha256:…") must still be read as semver — the
// Renovate default pins every image this way, so without it the rule never fires.
func TestLint_MajorImageBump_DigestPinned(t *testing.T) {
	t.Parallel()
	images := []api.ImageChange{{Name: "ghcr.io/app",
		From: "v1.9.0@sha256:" + strings.Repeat("a", 64), To: "v2.0.0@sha256:" + strings.Repeat("b", 64)}}
	n, w := countRule(Lint(nil, images, nil), "major-image-bump")
	if n != 1 {
		t.Fatalf("major-image-bump count = %d, want 1 for a digest-pinned major bump", n)
	}
	// The detail names the tags, not the 64-hex digests.
	if !strings.Contains(w.Detail, "v1.9.0 → v2.0.0") || strings.Contains(w.Detail, "sha256") {
		t.Errorf("detail should read by tag, got %q", w.Detail)
	}
}

func ociRepo(chart, tag string) map[string]any {
	return ociRepoAt("oci://ghcr.io/example/charts/"+chart, tag)
}

func ociRepoAt(url, tag string) map[string]any {
	return map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"spec":       map[string]any{"url": url, "ref": map[string]any{"tag": tag}},
	}
}

func helmReleaseChart(apiVersion, chart, version string) map[string]any {
	return map[string]any{
		"apiVersion": apiVersion,
		"spec":       map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": chart, "version": version}}},
	}
}

const fluxHelmAPI = "helm.toolkit.fluxcd.io/v2"

// TestLint_MajorRefBump: a major bump pinned on the Flux source object itself —
// an OCIRepository tag or a HelmRelease chart version — is flagged from that
// object's manifest, so a breaking chart upgrade surfaces even when no rendered
// child changed (the label-based rule sees nothing then).
func TestLint_MajorRefBump(t *testing.T) {
	t.Parallel()
	changes := []Change{
		{Status: "changed", Kind: "OCIRepository", Namespace: "o11y", Name: "kube-prometheus-stack",
			Old: ociRepo("kube-prometheus-stack", "88.6.1"), New: ociRepo("kube-prometheus-stack", "89.0.0")}, // major → source caution
		{Status: "changed", Kind: "OCIRepository", Namespace: "o11y", Name: "blackbox",
			Old: ociRepo("blackbox", "11.17.2"), New: ociRepo("blackbox", "11.18.0")}, // minor → none
		{Status: "changed", Kind: "HelmRelease", Namespace: "media", Name: "plex",
			Old: helmReleaseChart(fluxHelmAPI, "app-template", "3.5.1"), New: helmReleaseChart(fluxHelmAPI, "app-template", "4.0.0")}, // major → chart caution
		{Status: "changed", Kind: "HelmRelease", Namespace: "media", Name: "ranged",
			Old: helmReleaseChart(fluxHelmAPI, "app-template", "3.x"), New: helmReleaseChart(fluxHelmAPI, "app-template", "4.x")}, // a range → not semver, none
		{Status: "changed", Kind: "HelmRelease", Namespace: "other", Name: "not-flux",
			Old: helmReleaseChart("example.com/v1", "x", "1.0.0"), New: helmReleaseChart("example.com/v1", "x", "2.0.0")}, // a non-Flux CRD of the same name → none
		{Status: "added", Kind: "OCIRepository", Namespace: "o11y", Name: "new",
			New: ociRepo("new", "2.0.0")}, // an add has no before side
	}
	ws := Lint(changes, nil, nil)
	n, w := countRule(ws, "major-source-bump")
	if n != 1 || w.Resource != "OCIRepository o11y/kube-prometheus-stack" || !strings.Contains(w.Detail, "88.6.1 → 89.0.0") {
		t.Errorf("major-source-bump: count=%d %+v; want 1 on the kube-prometheus-stack OCIRepository", n, w)
	}
	n, w = countRule(ws, "major-chart-bump")
	if n != 1 || w.Resource != "HelmRelease media/plex" || !strings.Contains(w.Detail, "3.5.1 → 4.0.0") {
		t.Errorf("major-chart-bump: count=%d %+v; want 1 on the plex HelmRelease", n, w)
	}
}

// When the source object and the rendered children both show the same bump (the
// usual case: the render did pick the new chart up), it is reported once, from
// the source object, not again from the helm.sh/chart label.
func TestLint_MajorRefBump_DedupsLabelRule(t *testing.T) {
	t.Parallel()
	changes := []Change{
		// The HelmRelease pins "v4.0.0" while the rendered label reads "4.0.0": the
		// child is deduped through its producing parent, not by string equality.
		{Status: "changed", Kind: "HelmRelease", Namespace: "media", Name: "plex",
			Old: helmReleaseChart(fluxHelmAPI, "app-template", "v3.5.1"), New: helmReleaseChart(fluxHelmAPI, "app-template", "v4.0.0")},
		{Status: "changed", Kind: "Deployment", Namespace: "media", Name: "plex", Parent: "HelmRelease media/plex",
			OldChart: "app-template-3.5.1", NewChart: "app-template-4.0.0", Old: map[string]any{}, New: map[string]any{}},
		// An unrelated chart that happens to move through the same version pair
		// must still be reported: the dedup is by chart identity, not versions.
		{Status: "changed", Kind: "Deployment", Namespace: "db", Name: "pg", Parent: "HelmRelease db/pg",
			OldChart: "cloudnative-pg-3.5.1", NewChart: "cloudnative-pg-4.0.0", Old: map[string]any{}, New: map[string]any{}},
	}
	ws := Lint(changes, nil, nil)
	var got []string
	for _, w := range ws {
		if w.Rule == "major-chart-bump" {
			got = append(got, w.Resource)
		}
	}
	want := []string{"HelmRelease media/plex", "cloudnative-pg"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("major-chart-bump resources = %v, want %v (app-template once via the HelmRelease, cloudnative-pg via its label)", got, want)
	}
}

// A grouped Renovate PR bumping one chart across many HelmReleases is one
// finding, named on the first HelmRelease and counting the rest, and the label
// rule adds nothing on top.
func TestLint_MajorRefBump_GroupedIsOneFinding(t *testing.T) {
	t.Parallel()
	changes := make([]Change, 0, 6)
	for _, app := range []string{"plex", "sonarr", "radarr"} {
		changes = append(changes,
			Change{Status: "changed", Kind: "HelmRelease", Namespace: "media", Name: app,
				Old: helmReleaseChart(fluxHelmAPI, "app-template", "3.5.1"), New: helmReleaseChart(fluxHelmAPI, "app-template", "4.0.0")},
			Change{Status: "changed", Kind: "Deployment", Namespace: "media", Name: app, Parent: "HelmRelease media/" + app,
				OldChart: "app-template-3.5.1", NewChart: "app-template-4.0.0", Old: map[string]any{}, New: map[string]any{}},
		)
	}
	n, w := countRule(Lint(changes, nil, nil), "major-chart-bump")
	if n != 1 || w.Resource != "HelmRelease media/plex" {
		t.Fatalf("want one major-chart-bump on the first HelmRelease, got %d (last %+v)", n, w)
	}
	if !strings.Contains(w.Detail, "3.5.1 → 4.0.0 across 3 HelmReleases") {
		t.Errorf("detail should count the grouped objects, got %q", w.Detail)
	}
}

// Two OCI sources whose URLs end in the same name are different charts: grouping
// is by full URL, so each keeps its own finding (and its own release notes).
func TestLint_MajorRefBump_DistinctSourcesStayApart(t *testing.T) {
	t.Parallel()
	changes := []Change{
		{Status: "changed", Kind: "OCIRepository", Namespace: "a", Name: "app",
			Old: ociRepoAt("oci://ghcr.io/acme/charts/app", "1.0.0"), New: ociRepoAt("oci://ghcr.io/acme/charts/app", "2.0.0")},
		{Status: "changed", Kind: "OCIRepository", Namespace: "b", Name: "app",
			Old: ociRepoAt("oci://registry.example/other/app", "1.0.0"), New: ociRepoAt("oci://registry.example/other/app", "2.0.0")},
		// The same source pinned twice does fold.
		{Status: "changed", Kind: "OCIRepository", Namespace: "c", Name: "app",
			Old: ociRepoAt("oci://ghcr.io/acme/charts/app", "1.0.0"), New: ociRepoAt("oci://ghcr.io/acme/charts/app", "2.0.0")},
	}
	var got []string
	for _, w := range Lint(changes, nil, nil) {
		if w.Rule == "major-source-bump" {
			got = append(got, w.Resource)
		}
	}
	want := []string{"OCIRepository a/app", "OCIRepository b/app"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("major-source-bump resources = %v, want %v", got, want)
	}
}

// TestLint_MajorChartBump_BuildMetadata: Helm renders the helm.sh/chart label with
// build metadata sanitized ("1.0.0+build.5" → "1.0.0_build.5"). splitChart must
// still recognize the version (regex) and hand majorOf valid semver (de-sanitize),
// or the bump silently never fires.
func TestLint_MajorChartBump_BuildMetadata(t *testing.T) {
	t.Parallel()
	changes := []Change{
		{Status: "changed", Kind: "Deployment", Name: "a", Parent: "HelmRelease app",
			OldChart: "app-1.0.0_build.5", NewChart: "app-2.0.0_build.9", Old: map[string]any{}, New: map[string]any{}},
	}
	n, w := countRule(Lint(changes, nil, nil), "major-chart-bump")
	if n != 1 {
		t.Fatalf("major-chart-bump count = %d, want 1 (a build-metadata label must still fire)", n)
	}
	if w.Resource != "app" {
		t.Errorf("major-chart-bump resource = %q, want app", w.Resource)
	}
	// splitChart must un-sanitize the underscore back to a "+" so the version is
	// valid semver in the detail (and for majorOf).
	if _, ver := splitChart("app-1.0.0_build.5"); ver != "1.0.0+build.5" {
		t.Errorf("splitChart version = %q, want 1.0.0+build.5", ver)
	}
}

func TestMajorOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"1.15.0", 1, true},
		{"v2.0.0", 2, true},
		{"1.2.3-rc1", 1, true},
		{"1.2", 1, true}, // partial
		{"latest", 0, false},
		{"sha256:deadbeef", 0, false},
		{"20240131", 0, false},   // date tag (huge major)
		{"2024.01.31", 0, false}, // calver (major too large)
		{"3", 3, true},           // bare major now parses (postgres:16-style tags)
		{"16", 16, true},
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
	bump := [][2]string{{"v1.0.0", "v2.0.0"}, {"1.9.9", "2.0.0"}, {"2.0.0", "1.0.0"}, {"16", "17"}}
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
