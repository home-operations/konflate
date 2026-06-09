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
			Summary:  api.DiffSummary{Added: 2, Changed: 3, Removed: 1},
			Impact:   api.Impact{Resources: 6, Parents: 2, CRDs: 1},
			Warnings: []api.Warning{{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api", Detail: "replicas set to 0"}},
			Failures: []api.RenderFailure{{Parent: "HelmRelease media/plex", Message: "values don't meet the schema"}},
			Images:   []api.ImageChange{{Name: "ghcr.io/rook/ceph", From: "v1.14.9", To: "v1.15.0"}},
		},
	}
}

func TestSummaryMarkdown_GitHubAdmonitions(t *testing.T) {
	t.Parallel()
	md := summaryMarkdown(sampleSummaryEnv(), "https://k.example/#/pr/142", true)
	for _, want := range []string{
		"<!-- konflate:pr-142 -->",
		"### konflate — summary",
		"+2 added · 3 changed · −1 removed** — 6 resources · 2 apps · 1 CRD",
		"> [!CAUTION]",
		"> - `Deployment web/api` — replicas set to 0",
		"> [!WARNING]",
		"> - `HelmRelease media/plex` — values don't meet the schema",
		"| image | from | to |",
		"| `ghcr.io/rook/ceph` | `v1.14.9` | `v1.15.0` |",
		"[View the full rendered diff →](https://k.example/#/pr/142)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("github markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestSummaryMarkdown_PlainHasNoAdmonitions(t *testing.T) {
	t.Parallel()
	md := summaryMarkdown(sampleSummaryEnv(), "", false)
	if strings.Contains(md, "[!CAUTION]") || strings.Contains(md, "[!WARNING]") {
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
