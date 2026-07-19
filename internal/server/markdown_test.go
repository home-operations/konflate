package server

import (
	"net/http"
	"net/http/httptest"
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

func TestSummaryMarkdown_BlockingTierSeparateFromCaution(t *testing.T) {
	t.Parallel()
	env := api.DiffEnvelope{
		Status: api.JobReady,
		PR:     api.PR{Number: 7},
		Diff: &api.DiffResult{
			HeadSHA: "abc1234",
			Summary: api.DiffSummary{Changed: 1},
			Impact:  api.Impact{Resources: 1},
			Warnings: []api.Warning{
				{Level: api.LevelBlocking, Rule: "image-not-found", Resource: "Deployment web/api", Detail: "image ghcr.io/x:9.9.9 not found upstream"},
				{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api", Detail: "replicas set to 0"},
			},
		},
	}
	// Blocking → red [!CAUTION] "Blocker"; caution → amber [!WARNING], its own block.
	md := summaryMarkdown(env, "", true)
	for _, want := range []string{
		"> [!CAUTION]\n> **⛔ Blocker**",
		"> - `Deployment web/api` — image ghcr.io/x:9.9.9 not found upstream",
		"> [!WARNING]\n> **⚠ Caution**",
		"> - `Deployment web/api` — replicas set to 0",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("blocking markdown missing %q\n---\n%s", want, md)
		}
	}
	// The blocking finding must not be double-counted into the caution block — with
	// one caution the header stays singular ("Caution", never plural "Cautions").
	if strings.Contains(md, "**⚠ Cautions**") {
		t.Errorf("blocking warning leaked into the caution count:\n%s", md)
	}
	// Blocking (higher severity) renders before the caution block.
	if bi, ci := strings.Index(md, "⛔ Blocker"), strings.Index(md, "⚠ Caution"); bi < 0 || ci < 0 || bi > ci {
		t.Errorf("blocking block should render before caution (blocking=%d caution=%d)\n%s", bi, ci, md)
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
		// Alert colours match the list pills: caution = amber [!WARNING], failure = red [!CAUTION].
		"> [!WARNING]\n> **⚠ Caution**",
		"> - `Deployment web/api` — replicas set to 0",
		"> [!CAUTION]\n> **1 render failure**",
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

func TestSummaryMarkdown_Routine(t *testing.T) {
	t.Parallel()
	env := api.DiffEnvelope{
		Status: api.JobReady,
		PR:     api.PR{Number: 7},
		Diff: &api.DiffResult{
			HeadSHA: "abcdef1234567890",
			Summary: api.DiffSummary{Changed: 2},
			Impact:  api.Impact{Resources: 2, Parents: 1},
			Images:  []api.ImageChange{{Name: "ghcr.io/x", From: "1.0", To: "1.1"}},
			Routine: true,
		},
	}
	// GitHub flavour: a green [!TIP] naming itself, and — being routine — no
	// caution/failure blocks.
	md := summaryMarkdown(env, "", true)
	for _, want := range []string{"> [!TIP]", "**Routine**"} {
		if !strings.Contains(md, want) {
			t.Errorf("routine PR github markdown missing %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "[!CAUTION]") || strings.Contains(md, "[!WARNING]") {
		t.Errorf("a routine PR carries no caution/failure blocks:\n%s", md)
	}
	// Plain flavour: the bold line, no admonition syntax.
	plain := summaryMarkdown(env, "", false)
	if strings.Contains(plain, "[!TIP]") {
		t.Errorf("plain markdown must not use admonitions:\n%s", plain)
	}
	if !strings.Contains(plain, "**Routine**") {
		t.Errorf("plain routine line missing:\n%s", plain)
	}
}

func TestSummaryMarkdown_EscapesForgeText(t *testing.T) {
	t.Parallel()
	env := sampleSummaryEnv()
	// What a malicious PR might craft into a render error: a pipe (table break),
	// raw HTML (injection), and a newline (would split the list item).
	env.Diff.Failures = []api.RenderFailure{{
		Parent: "HelmRelease ns/app",
		// A pipe (table break), raw HTML, a newline (would split the list item), and
		// Markdown that — unescaped — would inject a clickable link, a remote image,
		// and a code span into konflate's own comment/check-run.
		Message: "boom | <script>alert(1)</script>\nsecond line [click](https://evil.example) ![x](https://evil.example/p.png) `code`",
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
	// The Markdown link/image syntax must be defanged: the brackets are
	// backslash-escaped, so no [text](url) link or ![alt](url) image forms.
	if strings.Contains(md, "[click](") || strings.Contains(md, "![x](") {
		t.Errorf("forge text must not inject a Markdown link or image:\n%s", md)
	}
	if !strings.Contains(md, `\[click\]`) || !strings.Contains(md, `\!\[x\]`) {
		t.Errorf("expected escaped link/image brackets:\n%s", md)
	}
	// A backtick must be escaped so it can't open a code span mid-message.
	if !strings.Contains(md, "\\`code\\`") {
		t.Errorf("expected escaped code-span backticks:\n%s", md)
	}
}

func TestMdInline_DefangsMarkdown(t *testing.T) {
	t.Parallel()
	// Backslash-escaped punctuation renders as the literal character in GFM, so a
	// link/image/code/emphasis attempt is inert while the text reads unchanged.
	got := mdInline("a [l](u) ![i](u) `c` *b* _e_ ~s~ | <x>")
	want := "a \\[l\\](u) \\!\\[i\\](u) \\`c\\` \\*b\\* \\_e\\_ \\~s\\~ \\| &lt;x&gt;"
	if got != want {
		t.Errorf("mdInline mismatch:\n got %q\nwant %q", got, want)
	}
	// Parens must pass through untouched: \(...\) is Forgejo's inline-math
	// delimiter, so escaping them turns prose into KaTeX there (#349).
	if got := mdInline("recreated (or Flux force is enabled)"); got != "recreated (or Flux force is enabled)" {
		t.Errorf("parens must not be escaped, got %q", got)
	}
	// A lone backslash is escaped first, so it can't consume a following escape.
	if got := mdInline(`\`); got != `\\` {
		t.Errorf("a backslash must be escaped, got %q", got)
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

// TestReviewURLFromRequest_RejectsInjection: the request-controlled host/scheme
// are embedded straight into a Markdown link, so a value carrying link-breakout
// characters must be rejected (fall back to the request Host, or drop the link),
// and a forged scheme clamped to https.
func TestReviewURLFromRequest_RejectsInjection(t *testing.T) {
	t.Parallel()
	// A well-formed forwarded host + proto → the normal review URL.
	r := httptest.NewRequest(http.MethodGet, "/api/prs/7/summary", nil)
	r.Header.Set("X-Forwarded-Host", "konflate.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := reviewURLFromRequest(r, 7); got != "https://konflate.example/#/pr/7" {
		t.Errorf("valid host/proto: got %q", got)
	}
	// An X-Forwarded-Host carrying Markdown link-breakout characters is ignored in
	// favour of the (clean) request Host, so no second link is injected.
	r2 := httptest.NewRequest(http.MethodGet, "/api/prs/7/summary", nil)
	r2.Host = "konflate.example"
	r2.Header.Set("X-Forwarded-Proto", "https")
	r2.Header.Set("X-Forwarded-Host", "evil.example) [inject](https://attacker.example")
	if got := reviewURLFromRequest(r2, 7); got != "https://konflate.example/#/pr/7" {
		t.Errorf("injected X-Forwarded-Host must fall back to the request Host, got %q", got)
	}
	// A forged X-Forwarded-Proto is clamped to https.
	r3 := httptest.NewRequest(http.MethodGet, "/api/prs/7/summary", nil)
	r3.Host = "konflate.example"
	r3.Header.Set("X-Forwarded-Proto", "javascript:alert(1)//")
	if got := reviewURLFromRequest(r3, 7); got != "https://konflate.example/#/pr/7" {
		t.Errorf("forged scheme must clamp to https, got %q", got)
	}
	// No usable host at all → no link (rather than a malformed one).
	r4 := httptest.NewRequest(http.MethodGet, "/api/prs/7/summary", nil)
	r4.Host = "bad host) with (spaces"
	if got := reviewURLFromRequest(r4, 7); got != "" {
		t.Errorf("an unusable host must yield no link, got %q", got)
	}
}
