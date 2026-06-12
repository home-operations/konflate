package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/home-operations/konflate/internal/api"
)

// konflateMarker is the hidden HTML comment tagging konflate's own PR comment,
// so comment write-back can find and edit it in place instead of posting a new
// one on each render. summaryMarkdown embeds it at the top; the forge Writer
// matches a comment body against it.
func konflateMarker(number int) string {
	return fmt.Sprintf("<!-- konflate:pr-%d -->", number)
}

// summaryMarkdown renders a PR's diff summary as a paste-ready Markdown block for
// posting back onto the pull request, prefixed with the konflate marker (a hidden
// HTML comment) so a poster can find and edit its own comment in place.
func summaryMarkdown(env api.DiffEnvelope, reviewURL string, admonitions bool) string {
	return konflateMarker(env.PR.Number) + "\n" + summaryMarkdownBody(env, reviewURL, admonitions)
}

// summaryMarkdownBody is the marker-less summary body. With admonitions=true it
// uses GitHub-flavoured alert blocks (> [!CAUTION] / > [!WARNING]); otherwise a
// plain bold-heading + bullet list that renders anywhere. Every forge-controlled
// value is escaped (see mdInline/mdCode) so a crafted resource name or a render
// error can't break the table or inject HTML. Exposed to a custom comment
// template as {{ .Summary }}.
func summaryMarkdownBody(env api.DiffEnvelope, reviewURL string, admonitions bool) string {
	var b strings.Builder
	b.WriteString("### konflate — summary\n")

	writeLink := func() {
		if reviewURL != "" {
			fmt.Fprintf(&b, "\n[View the full rendered diff →](%s)\n", reviewURL)
		}
	}

	if env.Status != api.JobReady || env.Diff == nil {
		switch env.Status {
		case api.JobError:
			fmt.Fprintf(&b, "\nRender failed: %s\n", mdInline(env.Error))
		default:
			b.WriteString("\n⏳ Still rendering — this updates once the diff is ready.\n")
		}
		writeLink()
		return b.String()
	}

	d := env.Diff

	// writeFooter closes the comment: the review link, then a subtle provenance
	// line naming the commit this summary reflects. The comment is edited in place
	// across pushes, so the rendered SHA is the only cue to which one it shows.
	writeFooter := func() {
		writeLink()
		if sha := shortSHA(d.HeadSHA); sha != "" {
			fmt.Fprintf(&b, "\n<sub>konflate · rendered `%s` · advisory, not a gate</sub>\n", sha)
		} else {
			b.WriteString("\n<sub>konflate · advisory, not a gate</sub>\n")
		}
	}
	// writeRefreshNote flags that the most recent re-render failed and we're showing
	// the last good diff, so a reviewer doesn't act on a stale render unaware.
	writeRefreshNote := func() {
		if env.RefreshError == "" {
			return
		}
		if admonitions {
			b.WriteString("\n> [!WARNING]\n> Couldn't refresh against the latest push — showing the last good render.\n")
		} else {
			b.WriteString("\n**⚠ Stale render** — couldn't refresh against the latest push; showing the last good render.\n")
		}
	}

	// Nothing rendered-changed (a docs/CI-only PR, or a Flux edit that nets to
	// nothing): say so plainly instead of a "+0 · 0 · −0 — 0 resources" line. Any
	// warning, failure, or image change means there's something worth showing.
	if d.Summary.Added == 0 && d.Summary.Changed == 0 && d.Summary.Removed == 0 &&
		len(d.Warnings) == 0 && len(d.Failures) == 0 && len(d.Images) == 0 {
		if admonitions {
			b.WriteString("\n> [!NOTE]\n> ✅ No rendered changes.\n")
		} else {
			b.WriteString("\n✅ No rendered changes.\n")
		}
		writeRefreshNote()
		writeFooter()
		return b.String()
	}
	// The content blocks, each rendered once so a custom comment template can place
	// them individually (commentTemplateData.Sections); the default body composes
	// the same blocks in order. The refresh note and footer are konflate's own
	// chrome and stay here.
	sec := summarySectionsFor(d, admonitions)
	appendSection := func(s string) {
		if s != "" {
			b.WriteString("\n" + s + "\n")
		}
	}
	appendSection(sec.Impact)
	writeRefreshNote()
	appendSection(sec.BlastRadius)
	appendSection(sec.Cautions)
	appendSection(sec.Failures)
	appendSection(sec.Images)
	writeFooter()
	return b.String()
}

// summarySections holds the summary's content blocks rendered individually, so a
// custom comment template can place them à la carte ({{ .Sections.Cautions }})
// instead of taking the whole {{ .Summary }}. Each is Markdown matching the
// forge's flavour (GitHub admonitions vs plain), or empty when that block has
// nothing to show. summaryMarkdownBody composes the same blocks for the default.
type summarySections struct {
	Impact      string // headline counts: +added · changed · −removed — N resources · …
	BlastRadius string // downstream apps transitively depending on the changed ones
	Cautions    string // danger-lint warnings
	Failures    string // resources that failed to render
	Images      string // container image changes (a Markdown table)
}

// summarySectionsFor renders each summary block for a ready diff (all-empty for a
// nil diff, e.g. an errored or still-rendering PR).
func summarySectionsFor(d *api.DiffResult, admonitions bool) summarySections {
	if d == nil {
		return summarySections{}
	}
	return summarySections{
		Impact:      sectionImpact(d, admonitions),
		BlastRadius: sectionBlastRadius(d),
		Cautions:    sectionCautions(d, admonitions),
		Failures:    sectionFailures(d, admonitions),
		Images:      sectionImages(d),
	}
}

// sectionImpact is the headline counts line. On the GitHub flavour it sits inside
// a [!NOTE] admonition; plain keeps it bare. Always non-empty (even +0).
func sectionImpact(d *api.DiffResult, admonitions bool) string {
	var impact strings.Builder
	fmt.Fprintf(&impact, "**+%d added · %d changed · −%d removed** — %d %s",
		d.Summary.Added, d.Summary.Changed, d.Summary.Removed,
		d.Impact.Resources, plural(d.Impact.Resources, "resource", "resources"))
	if d.Impact.Parents > 0 {
		fmt.Fprintf(&impact, " · %d %s", d.Impact.Parents, plural(d.Impact.Parents, "app", "apps"))
	}
	if d.Impact.CRDs > 0 {
		fmt.Fprintf(&impact, " · %d %s", d.Impact.CRDs, plural(d.Impact.CRDs, "CRD", "CRDs"))
	}
	if d.Truncated > 0 {
		fmt.Fprintf(&impact, " · %d not shown", d.Truncated)
	}
	if admonitions {
		return "> [!NOTE]\n> " + impact.String()
	}
	return impact.String()
}

func sectionCautions(d *api.DiffResult, admonitions bool) string {
	if len(d.Warnings) == 0 {
		return ""
	}
	var b strings.Builder
	if admonitions {
		// [!CAUTION] renders its own "Caution" heading — no redundant title line.
		b.WriteString("> [!CAUTION]\n")
		for _, wn := range d.Warnings {
			fmt.Fprintf(&b, "> - `%s` — %s\n", mdCode(wn.Resource), mdInline(wn.Detail))
		}
	} else {
		fmt.Fprintf(&b, "**⚠ %s**\n", plural(len(d.Warnings), "Caution", "Cautions"))
		for _, wn := range d.Warnings {
			fmt.Fprintf(&b, "- `%s` — %s\n", mdCode(wn.Resource), mdInline(wn.Detail))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sectionFailures(d *api.DiffResult, admonitions bool) string {
	if len(d.Failures) == 0 {
		return ""
	}
	var b strings.Builder
	title := fmt.Sprintf("%d render %s", len(d.Failures), plural(len(d.Failures), "failure", "failures"))
	if admonitions {
		fmt.Fprintf(&b, "> [!WARNING]\n> **%s**\n", title)
		for _, f := range d.Failures {
			fmt.Fprintf(&b, "> - `%s` — %s\n", mdCode(f.Parent), mdInline(f.Message))
		}
	} else {
		fmt.Fprintf(&b, "**⛔ Render failures (%d)**\n", len(d.Failures))
		for _, f := range d.Failures {
			fmt.Fprintf(&b, "- `%s` — %s\n", mdCode(f.Parent), mdInline(f.Message))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sectionImages(d *api.DiffResult) string {
	if len(d.Images) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Image changes**\n\n| image | from | to |\n|---|---|---|\n")
	for _, im := range d.Images {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n",
			mdCode(im.Name), mdCode(shortVer(im.From)), mdCode(shortVer(im.To)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeBlastRadius renders the blast-radius block: for each changed/failed app,
// how many downstream apps declare a transitive spec.dependsOn on it — the
// headline number a raw file diff can't show. Informational (like the image
// table), so plain bold in both flavours. It names a sample of direct
// dependents; the count and the sample add up to the headline
// ("12 dependents (a, b, c +9 more)"). No-op when nothing depends on anything.
func sectionBlastRadius(d *api.DiffResult) string {
	if len(d.BlastRadius) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Blast radius**\n")
	for _, br := range d.BlastRadius {
		fmt.Fprintf(&b, "- `%s` — %d %s", mdCode(br.Parent), br.Transitive, plural(br.Transitive, "dependent", "dependents"))
		shown := br.Direct
		if len(shown) == 0 {
			b.WriteString("\n")
			continue
		}
		const sample = 3
		if len(shown) > sample {
			shown = shown[:sample]
		}
		quoted := make([]string, len(shown))
		for i, s := range shown {
			quoted[i] = "`" + mdCode(s) + "`"
		}
		fmt.Fprintf(&b, " (%s", strings.Join(quoted, ", "))
		if more := br.Transitive - len(shown); more > 0 {
			fmt.Fprintf(&b, " +%d more", more)
		}
		b.WriteString(")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortSHA trims a git commit SHA to its 7-character display prefix; shorter or
// empty input is returned unchanged.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// mdInline escapes free text (warning details, render messages — possibly
// forge-controlled and multi-line) for safe inline Markdown: newlines collapse
// to spaces so a list item stays one item, and HTML/table metacharacters are
// neutralised.
func mdInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", `\|`).Replace(s)
}

// mdCode escapes a value rendered inside a `code span` (resource ids, image
// refs — already constrained charsets, but defended anyway): newlines flattened,
// backticks dropped (they would close the span) and table pipes escaped.
func mdCode(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "`", "'")
	return strings.ReplaceAll(s, "|", `\|`)
}

// shortVer trims an "algo:hexdigest" reference to "algo:<12 hex>…" so a
// digest-pinned image doesn't sprawl across the table; tags pass through.
func shortVer(v string) string {
	if v == "" {
		return "∅"
	}
	i := strings.IndexByte(v, ':')
	if i < 0 {
		return v
	}
	if hex := v[i+1:]; len(hex) > 12 && isHex(hex) {
		return v[:i+1] + hex[:12] + "…"
	}
	return v
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// reviewURLFromRequest reconstructs konflate's own public URL for a PR's review
// from the inbound request, honouring the usual reverse-proxy headers (konflate
// typically sits behind an ingress).
func reviewURLFromRequest(r *http.Request, number int) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s/#/pr/%d", scheme, host, number)
}
