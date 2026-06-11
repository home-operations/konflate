package server

import (
	"strings"
	"testing"

	"github.com/home-operations/konflate/internal/api"
)

func sampleSummaryEnv() api.DiffEnvelope {
	return api.DiffEnvelope{
		Status: api.JobReady,
		PR:     api.PR{Number: 142},
		Diff: &api.DiffResult{
			HeadSHA:  "1a2b3c4d5e6f78901234567890abcdef12345678",
			Summary:  api.DiffSummary{Added: 2, Changed: 3, Removed: 1},
			Impact:   api.Impact{Resources: 6, Parents: 2, CRDs: 1},
			Warnings: []api.Warning{{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api", Detail: "replicas set to 0"}},
			Failures: []api.RenderFailure{{Parent: "HelmRelease media/plex", Message: "values don't meet the schema"}},
			Images:   []api.ImageChange{{Name: "ghcr.io/rook/ceph", From: "v1.14.9", To: "v1.15.0"}},
			BlastRadius: []api.BlastRadiusEntry{
				{Parent: "Kustomization flux-system/cluster-apps", Transitive: 12, Direct: []string{
					"Kustomization flux-system/app-a", "Kustomization flux-system/app-b",
					"Kustomization flux-system/app-c", "Kustomization flux-system/app-d",
					"Kustomization flux-system/app-e",
				}},
				{Parent: "Kustomization flux-system/db", Transitive: 1, Direct: []string{"Kustomization flux-system/cache"}},
			},
		},
	}
}

func TestSummaryMarkdown_GitHubAdmonitions(t *testing.T) {
	t.Parallel()
	md := summaryMarkdown(sampleSummaryEnv(), "https://k.example/#/pr/142", true)
	for _, want := range []string{
		"<!-- konflate:pr-142 -->",
		"### konflate — summary",
		"> [!NOTE]",
		"+2 added · 3 changed · −1 removed** — 6 resources · 2 apps · 1 CRD",
		"> [!CAUTION]",
		"> - `Deployment web/api` — replicas set to 0",
		"> [!WARNING]",
		"> - `HelmRelease media/plex` — values don't meet the schema",
		"**Blast radius**",
		// Sample capped at 3 direct names; count + sample reconcile to the headline.
		"- `Kustomization flux-system/cluster-apps` — 12 dependents (`Kustomization flux-system/app-a`, `Kustomization flux-system/app-b`, `Kustomization flux-system/app-c` +9 more)",
		// Singular, no "+more" when the sample already covers the whole radius.
		"- `Kustomization flux-system/db` — 1 dependent (`Kustomization flux-system/cache`)",
		"| image | from | to |",
		"| `ghcr.io/rook/ceph` | `v1.14.9` | `v1.15.0` |",
		"[View the full rendered diff →](https://k.example/#/pr/142)",
		"konflate · rendered `1a2b3c4` · advisory, not a gate",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("github markdown missing %q\n---\n%s", want, md)
		}
	}
	// The [!CAUTION] block is its own heading; don't repeat the word in a title line.
	if strings.Contains(md, "> **Caution") {
		t.Errorf("caution admonition should not carry a redundant title:\n%s", md)
	}
}

func TestSummaryMarkdown_PlainHasNoAdmonitions(t *testing.T) {
	t.Parallel()
	md := summaryMarkdown(sampleSummaryEnv(), "", false)
	if strings.Contains(md, "[!NOTE]") || strings.Contains(md, "[!CAUTION]") || strings.Contains(md, "[!WARNING]") {
		t.Errorf("plain markdown must not use GitHub admonitions:\n%s", md)
	}
	for _, want := range []string{"**⚠ Caution**", "**⛔ Render failures (1)**"} {
		if !strings.Contains(md, want) {
			t.Errorf("plain markdown missing %q\n---\n%s", want, md)
		}
	}
	// No review URL → no link line.
	if strings.Contains(md, "View the full rendered diff") {
		t.Errorf("empty review URL should omit the link:\n%s", md)
	}
}

func TestSummaryMarkdown_EscapesForgeText(t *testing.T) {
	t.Parallel()
	env := sampleSummaryEnv()
	// What a malicious PR might craft into a render error: a pipe (table break),
	// raw HTML (injection), and a newline (would split the list item).
	env.Diff.Failures = []api.RenderFailure{{
		Parent:  "HelmRelease ns/app",
		Message: "boom | <script>alert(1)</script>\nsecond line",
	}}
	md := summaryMarkdown(env, "", true)
	if strings.Contains(md, "<script>") {
		t.Errorf("raw HTML must be escaped:\n%s", md)
	}
	if !strings.Contains(md, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped tags:\n%s", md)
	}
	if !strings.Contains(md, `boom \|`) {
		t.Errorf("expected escaped table pipe:\n%s", md)
	}
	if strings.Contains(md, "\nsecond line") {
		t.Errorf("a newline in a message must collapse to a space:\n%s", md)
	}
}

func TestSummaryMarkdown_NotReady(t *testing.T) {
	t.Parallel()
	md := summaryMarkdown(api.DiffEnvelope{Status: api.JobRunning, PR: api.PR{Number: 9}}, "https://k/#/pr/9", true)
	if !strings.Contains(md, "Still rendering") {
		t.Errorf("a running PR should say it's still rendering:\n%s", md)
	}
	if strings.Contains(md, "[!CAUTION]") {
		t.Errorf("no diff yet → no caution block:\n%s", md)
	}
}

func TestSummaryMarkdown_NoChanges(t *testing.T) {
	t.Parallel()
	env := api.DiffEnvelope{
		Status: api.JobReady,
		PR:     api.PR{Number: 5},
		Diff:   &api.DiffResult{HeadSHA: "deadbeefcafef00d"},
	}
	for _, admonitions := range []bool{true, false} {
		md := summaryMarkdown(env, "", admonitions)
		if !strings.Contains(md, "No rendered changes") {
			t.Errorf("admonitions=%v: expected a no-changes message:\n%s", admonitions, md)
		}
		if strings.Contains(md, "added ·") {
			t.Errorf("admonitions=%v: no-op summary must omit the impact line:\n%s", admonitions, md)
		}
		if !strings.Contains(md, "rendered `deadbee`") {
			t.Errorf("admonitions=%v: no-op summary should still carry provenance:\n%s", admonitions, md)
		}
	}
}

func TestSummaryMarkdown_RefreshError(t *testing.T) {
	t.Parallel()
	env := sampleSummaryEnv()
	env.RefreshError = "forge timeout"
	md := summaryMarkdown(env, "", true)
	if !strings.Contains(md, "showing the last good render") {
		t.Errorf("a refresh failure should be flagged:\n%s", md)
	}
	// The last-good diff is still rendered alongside the stale note.
	if !strings.Contains(md, "Deployment web/api") {
		t.Errorf("last-good diff content should still render:\n%s", md)
	}
}

func TestShortVer(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", "∅"},
		{"v1.15.0", "v1.15.0"},
		{"sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 12) + "…"},
	}
	for _, c := range cases {
		if got := shortVer(c.in); got != c.want {
			t.Errorf("shortVer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
