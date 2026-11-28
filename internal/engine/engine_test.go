package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/orchestrator"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/diff"
)

// res builds a minimal manifest map with kind + metadata, merging extra fields.
func res(kind, ns, name string, extra map[string]any) map[string]any {
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	m := map[string]any{"kind": kind, "metadata": meta}
	maps.Copy(m, extra)
	return m
}

// The capability overrides are the only HelmOptions konflate sets, and they
// reach flate only through renderCfg — an empty config must leave them unset
// so helm's own bundled capabilities apply.
func TestRenderCfg_HelmCapabilities(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Repo: "github://owner/repo"}
	opts := New(cfg, nil).(*flateEngine).renderCfg().HelmOptions
	if opts.KubeVersion != "" {
		t.Errorf("unset KubeVersion = %q, want empty", opts.KubeVersion)
	}
	if opts.APIVersions != "" {
		t.Errorf("unset APIVersions = %q, want empty", opts.APIVersions)
	}

	cfg.KubeVersion = "v1.33.4"
	cfg.HelmAPIVersions = "resource.k8s.io/v1/DeviceClass"
	opts = New(cfg, nil).(*flateEngine).renderCfg().HelmOptions
	if opts.KubeVersion != "v1.33.4" {
		t.Errorf("KubeVersion = %q, want v1.33.4", opts.KubeVersion)
	}
	if opts.APIVersions != "resource.k8s.io/v1/DeviceClass" {
		t.Errorf("APIVersions = %q, want resource.k8s.io/v1/DeviceClass", opts.APIVersions)
	}
}

func TestRenderUsable(t *testing.T) {
	t.Parallel()
	// A Complete run rendered (per-resource failures included); one that is
	// not failed fatally in discovery/bootstrap; an Absent run stands in for a
	// cluster the other side lacks.
	ok := orchestrator.Rendered{Complete: true, Result: &orchestrator.Result{}}
	fatal := orchestrator.Rendered{Result: &orchestrator.Result{}}
	absent := orchestrator.Rendered{Absent: true, Result: &orchestrator.Result{}}
	cases := []struct {
		name string
		runs []orchestrator.Rendered
		err  error
		want bool
	}{
		{"clean render", []orchestrator.Rendered{ok, ok}, nil, true},
		// Per-resource reconcile failures are advisory: flate still produced a
		// diff and the failures surface via Failures, so the diff must still
		// render — the regression this guards against.
		{"per-resource failures still usable", []orchestrator.Rendered{ok, ok}, errors.New("reconcile completed with 2 failure(s)"), true},
		// A cluster present on one side only renders against an Absent twin.
		{"absent base side", []orchestrator.Rendered{absent, ok}, nil, true},
		// A fatal discovery/bootstrap error on either side — nothing to show.
		{"fatal base bootstrap", []orchestrator.Rendered{fatal, ok}, errors.New("bootstrap: boom"), false},
		{"fatal head bootstrap", []orchestrator.Rendered{ok, fatal}, errors.New("bootstrap: boom"), false},
		{"input rejected before any tree ran", nil, errors.New("tree path is required"), false},
		// An incomplete render (DiffTimeout / cancellation) would mislead, so a
		// context error aborts even though both sides rendered.
		{"deadline exceeded", []orchestrator.Rendered{ok, ok}, fmt.Errorf("render: %w", context.DeadlineExceeded), false},
		{"canceled", []orchestrator.Rendered{ok, ok}, context.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderUsable(tc.runs, tc.err); got != tc.want {
				t.Errorf("renderUsable(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestClusterIDs covers the multi-cluster guard: a normal comparison is two
// runs of one cluster, while a repo root holding several clusters expands into
// a run per cluster and side, which Diff rejects.
func TestClusterIDs(t *testing.T) {
	t.Parallel()
	run := func(cluster string, side manifest.TreeSide) orchestrator.Rendered {
		return orchestrator.Rendered{ID: manifest.TreeID{Cluster: manifest.ClusterID(cluster), Side: side}}
	}
	if got := clusterIDs([]orchestrator.Rendered{run(".", manifest.TreeBase), run(".", manifest.TreeHead)}); !slices.Equal(got, []string{"."}) {
		t.Errorf("single cluster = %v, want [.]", got)
	}
	got := clusterIDs([]orchestrator.Rendered{
		run("clusters/prod", manifest.TreeBase), run("clusters/prod", manifest.TreeHead),
		run("clusters/dev", manifest.TreeBase), run("clusters/dev", manifest.TreeHead),
	})
	if !slices.Equal(got, []string{"clusters/dev", "clusters/prod"}) {
		t.Errorf("two clusters = %v, want sorted [clusters/dev clusters/prod]", got)
	}
}

func TestPairChanges(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: "app"}

	sameCM := func() map[string]any {
		return res("ConfigMap", "apps", "same", map[string]any{"data": map[string]any{"k": "v"}})
	}

	base := map[manifest.NamedResource][]map[string]any{
		parent: {
			res("Deployment", "apps", "web", map[string]any{"replicas": 3}),
			res("ConfigMap", "apps", "gone", map[string]any{"data": map[string]any{"x": "1"}}),
			sameCM(),
		},
	}
	head := map[manifest.NamedResource][]map[string]any{
		parent: {
			res("Deployment", "apps", "web", map[string]any{"replicas": 5}),
			res("Service", "apps", "svc", nil),
			sameCM(),
		},
	}

	got := pairChanges(base, head)

	// Unchanged ConfigMap "same" is dropped; the rest sort by kind then name.
	if len(got) != 3 {
		t.Fatalf("got %d changes, want 3: %+v", len(got), got)
	}
	want := []struct{ status, kind, name string }{
		{"removed", "ConfigMap", "gone"},
		{"changed", "Deployment", "web"},
		{"added", "Service", "svc"},
	}
	for i, w := range want {
		c := got[i]
		if c.Status != w.status || c.Kind != w.kind || c.Name != w.name {
			t.Errorf("change[%d] = {%s %s %s}, want {%s %s %s}", i, c.Status, c.Kind, c.Name, w.status, w.kind, w.name)
		}
		if c.Parent != "HelmRelease apps/app" {
			t.Errorf("change[%d].Parent = %q", i, c.Parent)
		}
	}
	if got[0].New != nil {
		t.Error("removed change should have nil New")
	}
	if got[2].Old != nil {
		t.Error("added change should have nil Old")
	}
	if got[1].Old["replicas"] != 3 || got[1].New["replicas"] != 5 {
		t.Errorf("changed Deployment old/new replicas = %v/%v, want 3/5", got[1].Old["replicas"], got[1].New["replicas"])
	}
}

// A resource pairs only with its counterpart under the same parent: the same
// identity under another parent is a different resource, so a move between
// parents reads as a removal plus an addition.
func TestPairChanges_PairsWithinParent(t *testing.T) {
	t.Parallel()
	a := manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: "a"}
	b := manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: "b"}
	cm := func() map[string]any {
		return res("ConfigMap", "apps", "shared", map[string]any{"data": map[string]any{"k": "v"}})
	}

	got := pairChanges(
		map[manifest.NamedResource][]map[string]any{a: {cm()}},
		map[manifest.NamedResource][]map[string]any{b: {cm()}},
	)
	if len(got) != 2 {
		t.Fatalf("got %d changes, want a removal and an addition: %+v", len(got), got)
	}
	if got[0].Status != "removed" || got[0].Parent != "HelmRelease apps/a" {
		t.Errorf("change[0] = {%s %s}, want removed under HelmRelease apps/a", got[0].Status, got[0].Parent)
	}
	if got[1].Status != "added" || got[1].Parent != "HelmRelease apps/b" {
		t.Errorf("change[1] = {%s %s}, want added under HelmRelease apps/b", got[1].Status, got[1].Parent)
	}
}

// Two documents of one identity under one parent are a render-authoring
// mistake; the later one is the one compared.
func TestPairChanges_LaterDocumentShadows(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "Kustomization", Namespace: "apps", Name: "app"}
	cm := func(v string) map[string]any {
		return res("ConfigMap", "apps", "cfg", map[string]any{"data": map[string]any{"k": v}})
	}
	got := pairChanges(
		map[manifest.NamedResource][]map[string]any{parent: {cm("same")}},
		map[manifest.NamedResource][]map[string]any{parent: {cm("other"), cm("same")}},
	)
	if len(got) != 0 {
		t.Errorf("the later head document equals the base, got %+v", got)
	}
}

// Identity includes the API group, so two kinds that share a name across
// groups are separate resources and never pair with each other.
func TestPairChanges_SeparatesAPIGroups(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "Kustomization", Namespace: "apps", Name: "app"}
	cert := func(apiVersion string) map[string]any {
		return res("Certificate", "apps", "tls", map[string]any{"apiVersion": apiVersion})
	}
	got := pairChanges(
		map[manifest.NamedResource][]map[string]any{parent: {cert("cert-manager.io/v1")}},
		map[manifest.NamedResource][]map[string]any{parent: {cert("networking.internal.knative.dev/v1alpha1")}},
	)
	if len(got) != 2 || got[0].Status == got[1].Status {
		t.Fatalf("got %+v, want one removal and one addition", got)
	}
}

// flate lends the rendered documents for the lifetime of the run, so pairing
// must normalize copies and leave its inputs as they were.
func TestPairChanges_LeavesInputsUntouched(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: "app"}
	secret := func(v string) map[string]any {
		m := res("Secret", "apps", "creds", map[string]any{"data": map[string]any{"password": v}})
		m["metadata"].(map[string]any)["labels"] = map[string]any{"helm.sh/chart": "app-1.0.0"}
		return m
	}
	base, head := secret("b2xk"), secret("bmV3")
	got := pairChanges(
		map[manifest.NamedResource][]map[string]any{parent: {base}},
		map[manifest.NamedResource][]map[string]any{parent: {head}},
	)
	if len(got) != 0 {
		t.Errorf("a redacted Secret value should not surface, got %+v", got)
	}
	if !reflect.DeepEqual(base, secret("b2xk")) || !reflect.DeepEqual(head, secret("bmV3")) {
		t.Errorf("pairChanges mutated its inputs: base=%v head=%v", base, head)
	}
}

// ConfigMap binaryData is compared by a size-and-hash summary: a changed blob
// still reads as a change, but the payload never reaches the diff.
func TestPairChanges_SummarizesBinaryData(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "Kustomization", Namespace: "apps", Name: "app"}
	cm := func(payload string) map[manifest.NamedResource][]map[string]any {
		return map[manifest.NamedResource][]map[string]any{parent: {
			res("ConfigMap", "apps", "blob", map[string]any{
				"binaryData": map[string]any{"file.bin": base64.StdEncoding.EncodeToString([]byte(payload))},
			}),
		}}
	}

	if got := pairChanges(cm("same"), cm("same")); len(got) != 0 {
		t.Errorf("identical binaryData should not surface, got %+v", got)
	}
	got := pairChanges(cm("old"), cm("new payload"))
	if len(got) != 1 || got[0].Status != "changed" {
		t.Fatalf("changed binaryData should surface as one changed resource, got %+v", got)
	}
	summary, _ := got[0].New["binaryData"].(map[string]any)["file.bin"].(string)
	if !strings.HasPrefix(summary, "<redacted binary data: 11 bytes sha256:") {
		t.Errorf("binaryData summary = %q, want the size and hash of the 11-byte payload", summary)
	}
}

// The chart label is stripped from the compared documents, so its value is
// carried on the change for the chart-version signal.
func TestPairChanges_KeepsChartLabel(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "x", Name: "x"}
	mk := func(m map[string]any) map[manifest.NamedResource][]map[string]any {
		return map[manifest.NamedResource][]map[string]any{parent: {m}}
	}
	got := pairChanges(
		mk(cmWith(map[string]any{"helm.sh/chart": "x-1.0.0"}, "old")),
		mk(cmWith(map[string]any{"helm.sh/chart": "x-2.0.0"}, "new")),
	)
	if len(got) != 1 || got[0].OldChart != "x-1.0.0" || got[0].NewChart != "x-2.0.0" {
		t.Fatalf("chart labels = %+v, want x-1.0.0 → x-2.0.0 on one change", got)
	}
}

func cmWith(labels map[string]any, data string) map[string]any {
	return map[string]any{
		"kind":     "ConfigMap",
		"metadata": map[string]any{"name": "cfg", "namespace": "default", "labels": labels},
		"data":     map[string]any{"key": data},
	}
}

func TestPairChanges_StripsChartNoise(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "x", Name: "x"}
	mk := func(m map[string]any) map[manifest.NamedResource][]map[string]any {
		return map[manifest.NamedResource][]map[string]any{parent: {m}}
	}

	// Identical content; only chart-bump labels differ → no change after strip.
	noiseOnly := pairChanges(
		mk(cmWith(map[string]any{"helm.sh/chart": "x-1.0.0", "app.kubernetes.io/version": "1.0.0"}, "same")),
		mk(cmWith(map[string]any{"helm.sh/chart": "x-2.0.0", "app.kubernetes.io/version": "2.0.0"}, "same")),
	)
	if len(noiseOnly) != 0 {
		t.Errorf("chart-noise-only diff should be suppressed, got %d: %+v", len(noiseOnly), noiseOnly)
	}

	// A real change still surfaces even with the noisy labels rotating too.
	real := pairChanges(
		mk(cmWith(map[string]any{"helm.sh/chart": "x-1.0.0"}, "old")),
		mk(cmWith(map[string]any{"helm.sh/chart": "x-2.0.0"}, "new")),
	)
	if len(real) != 1 || real[0].Status != "changed" {
		t.Fatalf("real change should surface as one changed resource, got %+v", real)
	}
}

// A real PEM certificate (self-signed, throwaway) so certToken exercises an
// actual base64-wrapped cert rather than a hand-waved prefix.
const samplePEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4wLjAuMC4wOjg0NDMwggr+8wIwCgYIKoZIzj0EAwIDSAAwRQIhAPb3JT8O
H8FZZjksZ4eXqIw3RkM2QcQ7QXqDjPj9aGiOAiBNqUVxqGqA2pSC8wL8w1y8wP99
xJ5p8c8wKp0Q1pCBkw==
-----END CERTIFICATE-----`

func TestCertToken(t *testing.T) {
	t.Parallel()
	b64 := base64.StdEncoding.EncodeToString([]byte(samplePEM))
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"raw cert", samplePEM, "<certificate>", true},
		{"base64 cert", b64, "<certificate>", true},
		{"raw key", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----", "<private key>", true},
		{"pkcs8 key", "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", "<private key>", true},
		{"csr", "-----BEGIN CERTIFICATE REQUEST-----\nabc\n-----END CERTIFICATE REQUEST-----", "<certificate request>", true},
		{"public key", "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----", "<public key>", true},
		{"plain string", "just a config value", "", false},
		{"base64 non-pem", base64.StdEncoding.EncodeToString([]byte("hello world not a cert at all")), "", false},
	}
	for _, tt := range cases {
		got, ok := certToken(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("%s: certToken() = (%q,%v), want (%q,%v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPairChanges_SuppressesRenderNoise(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "x", Name: "x"}
	mk := func(m map[string]any) map[manifest.NamedResource][]map[string]any {
		return map[manifest.NamedResource][]map[string]any{parent: {m}}
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	secret := func(tlsCrt string) map[string]any {
		return map[string]any{
			"kind":     "Secret",
			"metadata": map[string]any{"name": "webhook", "namespace": "kube-system"},
			"data":     map[string]any{"tls.crt": b64(tlsCrt), "tls.key": b64("-----BEGIN PRIVATE KEY-----\n" + tlsCrt + "\n-----END PRIVATE KEY-----")},
		}
	}
	crd := func(caCert string) map[string]any {
		return map[string]any{
			"kind":     "CustomResourceDefinition",
			"metadata": map[string]any{"name": "things.example.com"},
			"spec": map[string]any{"conversion": map[string]any{"webhook": map[string]any{
				"clientConfig": map[string]any{"caBundle": b64(caCert)},
			}}},
		}
	}

	// Two renders mint different certs (genSignedCert); nothing the PR did.
	certA := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----"
	certB := "-----BEGIN CERTIFICATE-----\nBBBB\n-----END CERTIFICATE-----"

	if got := pairChanges(mk(secret(certA)), mk(secret(certB))); len(got) != 0 {
		t.Errorf("rotating Secret cert should be suppressed, got %d: %+v", len(got), got)
	}
	if got := pairChanges(mk(crd(certA)), mk(crd(certB))); len(got) != 0 {
		t.Errorf("rotating CRD caBundle should be suppressed, got %d: %+v", len(got), got)
	}

	// A genuine key set change still surfaces despite the cert rotating too.
	withExtra := secret(certB)
	withExtra["data"].(map[string]any)["new.key"] = b64("value")
	got := pairChanges(mk(secret(certA)), mk(withExtra))
	if len(got) != 1 || got[0].Status != "changed" {
		t.Fatalf("added Secret key should surface as one changed resource, got %+v", got)
	}
}

func TestPairChanges_StripsVolatileChurn(t *testing.T) {
	t.Parallel()
	parent := manifest.NamedResource{Kind: "HelmRelease", Namespace: "apps", Name: "vol"}
	// A volsync ReplicationSource carrying both kinds of formerly-volatile
	// value: checksum/* annotations (incl. the plural checksum/secrets the old
	// exact-match list missed) and the spec.restic.unlock render timestamp.
	src := func(checksum, unlock string) map[manifest.NamedResource][]map[string]any {
		m := res("ReplicationSource", "apps", "data", map[string]any{
			"spec": map[string]any{"restic": map[string]any{"repository": "backups", "unlock": unlock}},
		})
		m["metadata"].(map[string]any)["annotations"] = map[string]any{
			"checksum/config":  checksum,
			"checksum/secrets": checksum,
		}
		return map[manifest.NamedResource][]map[string]any{parent: {m}}
	}

	// Rotating checksum/* annotations stay duplicate noise (the hashed content
	// change shows directly) — stripAttrs still deletes them, so
	// they alone must not surface a change.
	if got := pairChanges(src("aaaa", "20240101000000"), src("bbbb", "20240101000000")); len(got) != 0 {
		t.Errorf("rotating checksum/* should be stripped, got %d change(s): %+v", len(got), got)
	}

	// Since flate 0.3.4, spec.restic.unlock is NOT stripped: the deterministic
	// render clock pins `{{ now }}`, so both sides of a
	// RenderTrees render the identical timestamp and a difference here is a
	// genuine change (e.g. the chart altered its date format) that must surface.
	if got := pairChanges(src("aaaa", "20240101000000"), src("aaaa", "20240102000000")); len(got) != 1 {
		t.Errorf("a differing spec.restic.unlock is a genuine change under the deterministic clock, got %d change(s): %+v", len(got), got)
	}
}

func deploy(ns, name, image string) map[string]any {
	return res("Deployment", ns, name, map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "c", "image": image}},
		}}},
	})
}

// TestCollectImages covers the reference detection: a value under an image
// field counts with or without a version, while any other string counts only
// when it is a full repository reference.
func TestCollectImages(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  map[string]any
		want map[string][]string
	}{
		{"untagged container image", map[string]any{"image": "nginx"}, map[string][]string{"nginx": {""}}},
		// The untagged values below count only because of the field they sit
		// under: the same strings under any other field are not references.
		{
			"imageName field",
			map[string]any{"spec": map[string]any{"imageName": "ghcr.io/cloudnative-pg/postgresql"}},
			map[string][]string{"ghcr.io/cloudnative-pg/postgresql": {""}},
		},
		{
			"Image-suffixed field",
			map[string]any{"spec": map[string]any{"initImage": "busybox"}},
			map[string][]string{"busybox": {""}},
		},
		{
			"reference under an image object",
			map[string]any{"image": map[string]any{"reference": "nginx", "pullPolicy": "IfNotPresent"}},
			map[string][]string{"nginx": {""}},
		},
		{
			"reference outside an image field",
			map[string]any{"source": map[string]any{"reference": "nginx"}},
			map[string][]string{},
		},
		{
			"image field inside a list",
			map[string]any{"containers": []any{map[string]any{"image": "busybox"}}},
			map[string][]string{"busybox": {""}},
		},
		{
			"full reference in an arbitrary field",
			map[string]any{"env": []any{map[string]any{"name": "SIDECAR", "value": "ghcr.io/example/sidecar:2"}}},
			map[string][]string{"ghcr.io/example/sidecar": {"2"}},
		},
		{
			"non-references in arbitrary fields",
			map[string]any{"data": map[string]any{
				"url":      "https://example.com/path:8080",
				"path":     "/usr/local/bin:/usr/bin",
				"template": "${REGISTRY}/app:1",
				"short":    "nginx:1.25",
				"untagged": "ghcr.io/example/app",
			}},
			map[string][]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectImages(tc.doc)
			if !maps.EqualFunc(got, tc.want, slices.Equal[[]string]) {
				t.Errorf("collectImages() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImageChanges(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{
		{Status: "changed", Kind: "Deployment", Namespace: "apps", Name: "web",
			Old: deploy("apps", "web", "ghcr.io/app:1.0"), New: deploy("apps", "web", "ghcr.io/app:2.0")},
		{Status: "added", Kind: "Deployment", Namespace: "apps", Name: "new",
			Old: nil, New: deploy("apps", "new", "ghcr.io/tool:5")},
	}

	got := imageChanges(changes)
	if len(got) != 2 {
		t.Fatalf("got %d image changes, want 2: %+v", len(got), got)
	}
	// sorted by Name: ghcr.io/app then ghcr.io/tool
	upgrade := got[0]
	if upgrade.Name != "ghcr.io/app" || upgrade.From != "1.0" || upgrade.To != "2.0" {
		t.Errorf("upgrade = %+v, want ghcr.io/app 1.0→2.0", upgrade)
	}
	if len(upgrade.Refs) != 1 || upgrade.Refs[0] != "Deployment apps/web" {
		t.Errorf("upgrade.Refs = %v", upgrade.Refs)
	}
	added := got[1]
	if added.Name != "ghcr.io/tool" || added.From != "" || added.To != "5" {
		t.Errorf("added = %+v, want ghcr.io/tool ''→5", added)
	}
}

// deployWithInit builds a Deployment with a main container and (when init != "")
// an init container, so a single resource can reference one repo at two tags.
func deployWithInit(ns, name, main, init string) map[string]any {
	spec := map[string]any{"containers": []any{map[string]any{"name": "c", "image": main}}}
	if init != "" {
		spec["initContainers"] = []any{map[string]any{"name": "i", "image": init}}
	}
	return res("Deployment", ns, name, map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": spec}},
	})
}

// TestImageChanges_SameRepoMultipleTags guards against the last-write-wins
// collapse: a repository referenced at two tags at once (a main and an init
// container) must not be flattened to one version and reported as a transition
// no container underwent. Here the PR only deletes the init container (which
// pinned the newer tag) and leaves the main container's tag untouched, so the
// one honest change is the removal of the init's tag — never a "1.26 → 1.25"
// downgrade of the main container.
func TestImageChanges_SameRepoMultipleTags(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{{
		Status: "changed", Kind: "Deployment", Namespace: "apps", Name: "web",
		Old: deployWithInit("apps", "web", "ghcr.io/app:1.25", "ghcr.io/app:1.26"),
		New: deployWithInit("apps", "web", "ghcr.io/app:1.25", ""),
	}}
	got := imageChanges(changes)
	if len(got) != 1 {
		t.Fatalf("got %d image changes, want 1 (the init tag removed): %+v", len(got), got)
	}
	if c := got[0]; c.Name != "ghcr.io/app" || c.From != "1.26" || c.To != "" {
		t.Errorf("got %+v, want ghcr.io/app 1.26→'' (a removal), not a fabricated downgrade", c)
	}
}

// TestImageChanges_RenameMergesTransition: a chart restructure removes the old
// resource (pure removal of the image) and adds a differently-named one (pure
// addition). The two halves must reconcile into the single real version bump the
// cluster undergoes, not read as one image vanishing and another appearing.
func TestImageChanges_RenameMergesTransition(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{
		{Status: "removed", Kind: "Deployment", Namespace: "rook", Name: "old-provisioner",
			Old: deploy("rook", "old-provisioner", "quay.io/cephcsi/cephcsi:v3.16.2"), New: nil},
		{Status: "added", Kind: "Deployment", Namespace: "rook", Name: "new-provisioner",
			Old: nil, New: deploy("rook", "new-provisioner", "quay.io/cephcsi/cephcsi:v3.17.0")},
	}
	got := imageChanges(changes)
	if len(got) != 1 {
		t.Fatalf("got %d image changes, want 1 merged transition: %+v", len(got), got)
	}
	c := got[0]
	if c.Name != "quay.io/cephcsi/cephcsi" || c.From != "v3.16.2" || c.To != "v3.17.0" {
		t.Errorf("got %+v, want cephcsi v3.16.2→v3.17.0 (the real bump)", c)
	}
	if len(c.Refs) != 2 {
		t.Errorf("merged refs should union both resources, got %v", c.Refs)
	}
}

// TestImageChanges_RelocationDropped: an image at the same version on both sides,
// split across a rename, is a pure relocation — the running image is unchanged,
// so it must not appear in the Image changes list at all.
func TestImageChanges_RelocationDropped(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{
		{Status: "removed", Kind: "Deployment", Namespace: "rook", Name: "old",
			Old: deploy("rook", "old", "registry.k8s.io/sig-storage/csi-resizer:v2.1.0"), New: nil},
		{Status: "added", Kind: "Deployment", Namespace: "rook", Name: "new",
			Old: nil, New: deploy("rook", "new", "registry.k8s.io/sig-storage/csi-resizer:v2.1.0")},
	}
	if got := imageChanges(changes); len(got) != 0 {
		t.Fatalf("a same-version relocation is not an image change, got %+v", got)
	}
}

// TestImageChanges_AmbiguousNotMerged: two removals and one addition of the same
// repo are ambiguous to pair, so they stay discrete — reconciliation must not
// fabricate a transition, mirroring TestImageChanges_SameRepoMultipleTags.
func TestImageChanges_AmbiguousNotMerged(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{
		{Status: "removed", Kind: "Deployment", Namespace: "a", Name: "r1",
			Old: deploy("a", "r1", "reg/x:1.0"), New: nil},
		{Status: "removed", Kind: "Deployment", Namespace: "a", Name: "r2",
			Old: deploy("a", "r2", "reg/x:1.1"), New: nil},
		{Status: "added", Kind: "Deployment", Namespace: "a", Name: "n1",
			Old: nil, New: deploy("a", "n1", "reg/x:2.0")},
	}
	got := imageChanges(changes)
	if len(got) != 3 {
		t.Fatalf("ambiguous repo must stay discrete, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.From != "" && c.To != "" {
			t.Errorf("no merged transition expected for an ambiguous repo, got %+v", c)
		}
	}
}

// TestImageChanges_MergedMoveFoldsIntoRealBump: one resource bumps an image in
// place (A→B) while a rename relocates the same image at the same versions on
// another (a pure removal at A + a pure addition at B). The reconciled move
// (A→B) coincides with the real bump, so it must fold into that single entry
// (unioning refs) — not append a duplicate that lists the image, and flags its
// major bump, twice.
func TestImageChanges_MergedMoveFoldsIntoRealBump(t *testing.T) {
	t.Parallel()
	changes := []diff.Change{
		{Status: "changed", Kind: "Deployment", Namespace: "apps", Name: "web",
			Old: deploy("apps", "web", "reg/app:1.0.0"), New: deploy("apps", "web", "reg/app:2.0.0")},
		{Status: "removed", Kind: "Deployment", Namespace: "apps", Name: "old",
			Old: deploy("apps", "old", "reg/app:1.0.0"), New: nil},
		{Status: "added", Kind: "Deployment", Namespace: "apps", Name: "new",
			Old: nil, New: deploy("apps", "new", "reg/app:2.0.0")},
	}
	got := imageChanges(changes)
	if len(got) != 1 {
		t.Fatalf("a merged move coinciding with a real bump must fold into one entry, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Name != "reg/app" || c.From != "1.0.0" || c.To != "2.0.0" {
		t.Errorf("got %+v, want reg/app 1.0.0→2.0.0", c)
	}
	if len(c.Refs) != 3 {
		t.Errorf("folded entry should union all three referencing resources, got %v", c.Refs)
	}
}

// TestImageChanges_DigestPinnedKeepsTag: a Renovate-style pin ("tag@sha256:…")
// must keep the tag in the version — flate's image.Split would reduce both sides
// to opaque digests, hiding the human-readable bump and starving the semver
// major-bump check.
func TestImageChanges_DigestPinnedKeepsTag(t *testing.T) {
	t.Parallel()
	const (
		oldRef = "ghcr.io/home-operations/sonarr:4.0.19.3009@sha256:67815effd2ea18398ffc2011d974b8b1cd3939eec5aa74688ffbf371ff65dd84"
		newRef = "ghcr.io/home-operations/sonarr:4.0.19.3011@sha256:47d56de90c81eb2a8f0d36771328a95e7299609579b88db82b1473c314f26a47"
	)
	changes := []diff.Change{{Status: "changed", Kind: "Deployment", Namespace: "default", Name: "sonarr",
		Old: deploy("default", "sonarr", oldRef), New: deploy("default", "sonarr", newRef)}}
	got := imageChanges(changes)
	if len(got) != 1 {
		t.Fatalf("got %d image changes, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Name != "ghcr.io/home-operations/sonarr" ||
		c.From != "4.0.19.3009@sha256:67815effd2ea18398ffc2011d974b8b1cd3939eec5aa74688ffbf371ff65dd84" ||
		c.To != "4.0.19.3011@sha256:47d56de90c81eb2a8f0d36771328a95e7299609579b88db82b1473c314f26a47" {
		t.Errorf("got %+v, want the tag kept ahead of the digest on both sides", c)
	}
}

func TestSplitRef(t *testing.T) {
	t.Parallel()
	const digest = "sha256:67815effd2ea18398ffc2011d974b8b1cd3939eec5aa74688ffbf371ff65dd84"
	cases := []struct{ ref, name, version string }{
		{"ghcr.io/app:1.2.3", "ghcr.io/app", "1.2.3"},
		{"ghcr.io/app@" + digest, "ghcr.io/app", digest},
		{"ghcr.io/app:1.2.3@" + digest, "ghcr.io/app", "1.2.3@" + digest},
		{"localhost:5000/app:v2", "localhost:5000/app", "v2"}, // a registry port is not the version
		{"ghcr.io/app", "ghcr.io/app", ""},
		{"not a ref", "not a ref", ""},
	}
	for _, tc := range cases {
		name, version := splitRef(tc.ref)
		if name != tc.name || version != tc.version {
			t.Errorf("splitRef(%q) = (%q, %q), want (%q, %q)", tc.ref, name, version, tc.name, tc.version)
		}
	}
}

func TestDropFailedParents(t *testing.T) {
	t.Parallel()
	// A HelmRelease whose head render timed out: base produced its resources, head
	// produced none, so they pair as "removed" — a phantom deletion of every CRD.
	// Plus a genuine change under a healthy parent.
	const failed = "HelmRelease kube-system/snapshot-controller"
	changes := []diff.Change{
		{Status: "removed", Kind: "CustomResourceDefinition", Name: "volumesnapshots.snapshot.storage.k8s.io", Parent: failed},
		{Status: "removed", Kind: "ClusterRole", Name: "snapshot-controller", Parent: failed},
		{Status: "changed", Kind: "Deployment", Namespace: "media", Name: "plex", Parent: "HelmRelease media/plex"},
	}
	failures := []api.RenderFailure{{Parent: failed, Message: "chart source OCIRepository/kube-system/snapshot-controller not ready: timeout"}}

	got := dropFailedParents(changes, failures)
	if len(got) != 1 || got[0].Parent != "HelmRelease media/plex" {
		t.Fatalf("phantom changes under a failed parent must be dropped; kept %+v", got)
	}
	// No failures → every change passes through untouched.
	if all := dropFailedParents(changes, nil); len(all) != len(changes) {
		t.Errorf("with no failures, all %d changes should survive; got %d", len(changes), len(all))
	}
}

func TestRenderFailures_UnionsBothSidesAndDedups(t *testing.T) {
	t.Parallel()
	base := map[manifest.NamedResource]orchestrator.StatusInfo{
		{Kind: "HelmRelease", Namespace: "a", Name: "x"}: {Message: "base failed"},
	}
	head := map[manifest.NamedResource]orchestrator.StatusInfo{
		{Kind: "HelmRelease", Namespace: "a", Name: "x"}: {Message: "also failed on head"}, // same parent → deduped
		{Kind: "HelmRelease", Namespace: "b", Name: "y"}: {Message: "head only"},
	}
	got := renderFailures(base, head)
	if len(got) != 2 {
		t.Fatalf("failures from both sides should union + dedup by parent; got %+v", got)
	}
	if got[0].Parent != "HelmRelease a/x" || got[1].Parent != "HelmRelease b/y" {
		t.Errorf("expected sort by parent; got %+v", got)
	}
	if got[0].Message != "base failed" {
		t.Errorf("first side's message should win for a parent that failed on both; got %q", got[0].Message)
	}
}
