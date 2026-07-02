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

// summaryMarkdownBody is the marker-less summary body. It opens with an H3 title
// (### konflate — summary); with admonitions=true the sections use GitHub-flavoured
// alert blocks (> [!TIP] / > [!CAUTION] / > [!WARNING]), otherwise plain bold-subheading bullet
// lists that render anywhere. Every forge-controlled
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
	appendSection(sec.Routine)
	writeRefreshNote()
	appendSection(sec.BlastRadius)
	appendSection(sec.Blocking)
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
	Routine     string // image/chart-version-only bump, nothing flagged (green [!TIP])
	BlastRadius string // downstream apps transitively depending on the changed ones
	Blocking    string // blocking-tier warnings — fail the check (red [!CAUTION])
	Cautions    string // caution-tier warnings — advisory (amber [!WARNING])
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
		Routine:     sectionRoutine(d, admonitions),
		BlastRadius: sectionBlastRadius(d),
		Blocking:    sectionBlocking(d, admonitions),
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

// sectionRoutine surfaces a routine PR — only container-image and chart-version
// changes, with nothing flagged. The GitHub flavour uses a green [!TIP], matching
// the routine list pill's colour; plain keeps a bold line. Empty unless the diff
// is routine. Like the pill, it describes the diff's shape, not runtime safety.
func sectionRoutine(d *api.DiffResult, admonitions bool) string {
	if !d.Routine {
		return ""
	}
	const msg = "**Routine** — only container-image and chart-version changes; nothing else changed."
	if admonitions {
		return "> [!TIP]\n> " + msg
	}
	return msg
}

// sectionBlocking renders the blocking-tier warnings — findings that fail the
// check (see checkConclusion). Red [!CAUTION], the top of the ramp, above the
// amber cautions. Empty unless a rule emitted a LevelBlocking warning (none do
// yet).
// mdItem is one "- `code` — detail" line in a summary block.
type mdItem struct{ code, detail string }

// mdBlock renders a summary block: a header — a GitHub admonition (> [!ALERT] +
// bold line) when admonitions, else a plain bold line — then one "- `code` —
// detail" item per entry (mdCode/mdInline escape the forge-controlled halves).
// The admonition and plain headers can differ: the plain form carries the
// severity glyph the admonition box would otherwise supply. Empty when no items.
func mdBlock(admonitions bool, alert, admonitionHeader, plainHeader string, items []mdItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	prefix := "- "
	if admonitions {
		fmt.Fprintf(&b, "> [!%s]\n> **%s**\n", alert, admonitionHeader)
		prefix = "> - "
	} else {
		fmt.Fprintf(&b, "**%s**\n", plainHeader)
	}
	for _, it := range items {
		fmt.Fprintf(&b, "%s`%s` — %s\n", prefix, mdCode(it.code), mdInline(it.detail))
	}
	return strings.TrimRight(b.String(), "\n")
}

// warningItems maps warnings to block items (Resource → code, Detail → detail).
func warningItems(ws []api.Warning) []mdItem {
	items := make([]mdItem, len(ws))
	for i, wn := range ws {
		items[i] = mdItem{code: wn.Resource, detail: wn.Detail}
	}
	return items
}

func sectionBlocking(d *api.DiffResult, admonitions bool) string {
	ws := api.WarningsByLevel(d.Warnings, api.LevelBlocking)
	// Red [!CAUTION] — a blocker is the top of the severity ramp.
	h := fmt.Sprintf("⛔ %s", plural(len(ws), "Blocker", "Blockers"))
	return mdBlock(admonitions, "CAUTION", h, h, warningItems(ws))
}

func sectionCautions(d *api.DiffResult, admonitions bool) string {
	ws := api.WarningsByLevel(d.Warnings, api.LevelCaution)
	// Amber [!WARNING] to match the caution list pill's colour (a notch below the
	// red of a blocker or render failure); the alert header reads "Warning".
	h := fmt.Sprintf("⚠ %s", plural(len(ws), "Caution", "Cautions"))
	return mdBlock(admonitions, "WARNING", h, h, warningItems(ws))
}

func sectionFailures(d *api.DiffResult, admonitions bool) string {
	items := make([]mdItem, len(d.Failures))
	for i, f := range d.Failures {
		items[i] = mdItem{code: f.Parent, detail: f.Message}
	}
	// Red [!CAUTION] like a blocker; the plain form adds the ⛔ glyph the box drops.
	admHeader := fmt.Sprintf("%d render %s", len(d.Failures), plural(len(d.Failures), "failure", "failures"))
	plainHeader := fmt.Sprintf("⛔ Render failures (%d)", len(d.Failures))
	return mdBlock(admonitions, "CAUTION", admHeader, plainHeader, items)
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
// to spaces so a list item stays one item, HTML/table metacharacters are
// neutralised, and Markdown inline punctuation is backslash-escaped. The last
// part matters because this text lands in konflate's OWN trusted PR comment and
// check-run: a render error echoing a fork's template could otherwise inject a
// clickable link, a remote image, a code span, or emphasis into that authored
// content. GFM strips the backslash on render, so the escapes are invisible.
func mdInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return mdInlineReplacer.Replace(s)
}

// mdInlineReplacer runs a single non-overlapping pass (so its own output is never
// re-escaped): HTML entities for <, >, &; a \| for the table pipe; and a
// backslash before every Markdown inline metacharacter — the link/image brackets
// and parens, the code-span backtick, the emphasis/strikethrough runs, the image
// bang, and the backslash itself (escaped first so it can't consume a following
// escape). Bare autolinked URLs are left alone: their destination is visible, so
// they aren't a spoofing vector the way [text](hidden-url) is.
var mdInlineReplacer = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", "|", `\|`,
	`\`, `\\`, "`", "\\`", "[", `\[`, "]", `\]`, "(", `\(`, ")", `\)`,
	"*", `\*`, "_", `\_`, "~", `\~`, "!", `\!`,
)

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
	scheme := schemeHTTPS
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		first, _, _ := strings.Cut(p, ",")
		scheme = strings.TrimSpace(first)
	} else if r.TLS == nil {
		scheme = "http"
	}
	// The scheme and host are request-controlled (X-Forwarded-* / Host), and this
	// URL is embedded straight into a Markdown link — a value carrying `)` or `[`
	// would break out and inject a second link. Clamp the scheme to http/https and
	// require a plain host[:port]; otherwise omit the link rather than emit a
	// malformed/injected one.
	if scheme != "http" && scheme != schemeHTTPS {
		scheme = schemeHTTPS
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		if first, _, _ := strings.Cut(xfh, ","); validHost(strings.TrimSpace(first)) {
			host = strings.TrimSpace(first)
		}
	}
	if !validHost(host) {
		return ""
	}
	return fmt.Sprintf("%s://%s/#/pr/%d", scheme, host, number)
}

// validHost reports whether h is a plain host[:port] (or a bracketed IPv6 literal)
// with no character that could break out of the Markdown link it is embedded in —
// only letters, digits, and the host punctuation . - : [ ].
func validHost(h string) bool {
	if h == "" || len(h) > 260 {
		return false
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}
