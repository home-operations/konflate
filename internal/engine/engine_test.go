package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/home-operations/flate/pkg/manifest"
	"github.com/home-operations/flate/pkg/orchestrator"

	"github.com/home-operations/konflate/internal/diff"
)

// res builds a minimal manifest map with kind + metadata, merging extra fields.
func res(kind, ns, name string, extra map[string]any) map[string]any {
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	m := map[string]any{"kind": kind, "metadata": meta}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestRenderUsable(t *testing.T) {
	t.Parallel()
	ok := &orchestrator.Result{} // a non-nil Result = that side bootstrapped and rendered
	cases := []struct {
		name       string
		base, head orchestrator.Rendered
		err        error
		want       bool
	}{
		{"clean render", rendered(ok), rendered(ok), nil, true},
		// Per-resource reconcile failures are advisory: flate still produced a
		// diff (Result non-nil) and the failures surface via Failures, so the
		// diff must still render — the regression this guards against.
		{"per-resource failures still usable", rendered(ok), rendered(ok), errors.New("reconcile completed with 2 failure(s)"), true},
		// A nil Result is a fatal Bootstrap error on that side — nothing to show.
		{"fatal base bootstrap", rendered(nil), rendered(ok), errors.New("bootstrap: boom"), false},
		{"fatal head bootstrap", rendered(ok), rendered(nil), errors.New("bootstrap: boom"), false},
		// An incomplete render (DiffTimeout / cancellation) would mislead, so a
		// context error aborts even though Result is non-nil.
		{"deadline exceeded", rendered(ok), rendered(ok), fmt.Errorf("render: %w", context.DeadlineExceeded), false},
		{"canceled", rendered(ok), rendered(ok), context.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderUsable(tc.base, tc.head, tc.err); got != tc.want {
				t.Errorf("renderUsable(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// rendered wraps a Result in the orchestrator.Rendered shape renderUsable reads
// (the embedded *Orchestrator is unused by the gate).
func rendered(result *orchestrator.Result) orchestrator.Rendered {
	return orchestrator.Rendered{Result: result}
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
	// A volsync ReplicationSource whose only between-render differences are
	// volatile noise: rotating checksum/* annotations (incl. the plural
	// checksum/secrets the old exact-match list missed) and the render-clock
	// spec.restic.unlock timestamp. Both must be stripped, so no change surfaces.
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
	if got := pairChanges(src("aaaa", "20240101000000"), src("bbbb", "20240102000000")); len(got) != 0 {
		t.Errorf("rotating checksum/* + spec.restic.unlock should be stripped, got %d change(s): %+v", len(got), got)
	}
}

func deploy(ns, name, image string) map[string]any {
	return res("Deployment", ns, name, map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "c", "image": image}},
		}}},
	})
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

// splitImageRef's behavior now lives in flate's image.Split (tested in
// flate); konflate's collectImages composes image.Extract + image.Split,
// exercised end to end by TestImageChanges above.
