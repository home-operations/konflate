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
func summaryMarkdown(env api.DiffEnvelope, reviewURL string, admonitions bool, version string) string {
	return konflateMarker(env.PR.Number) + "\n" + summaryMarkdownBody(env, reviewURL, admonitions, version)
}

// summaryMarkdownBody is the marker-less summary body. It carries no heading —
// the footer names konflate and its version — and with admonitions=true the sections use GitHub-flavoured
// alert blocks (> [!TIP] / > [!CAUTION] / > [!WARNING]), otherwise plain bold-subheading bullet
// lists that render anywhere. Every forge-controlled
// value is escaped (see mdInline/mdCode) so a crafted resource name or a render
// error can't break the table or inject HTML. Exposed to a custom comment
// template as {{ .Summary }}.
func summaryMarkdownBody(env api.DiffEnvelope, reviewURL string, admonitions bool, version string) string {
	var b strings.Builder

	// writeFooter closes the comment with one small line: konflate's build version
	// (when stamped), provenance (the commit this summary reflects — the comment is
	// edited in place across pushes, so the rendered SHA is the only cue to which
	// one it shows) and the review link.
	writeFooter := func(headSHA string) {
		parts := []string{strings.TrimSpace("konflate " + version)}
		if sha := shortSHA(headSHA); sha != "" {
			parts = append(parts, fmt.Sprintf("rendered `%s`", sha))
		}
		if reviewURL != "" {
			parts = append(parts, fmt.Sprintf("[full diff →](%s)", reviewURL))
		}
		fmt.Fprintf(&b, "\n<sub>%s</sub>\n", strings.Join(parts, " · "))
	}

	if env.Status != api.JobReady || env.Diff == nil {
		switch env.Status {
		case api.JobError:
			fmt.Fprintf(&b, "\nRender failed: %s\n", mdInline(env.Error))
		default:
			b.WriteString("\n⏳ Still rendering; this updates once the diff is ready.\n")
		}
		writeFooter("")
		return b.String()
	}

	d := env.Diff
	// writeRefreshNote flags that the most recent re-render failed and we're showing
	// the last good diff, so a reviewer doesn't act on a stale render unaware.
	writeRefreshNote := func() {
		if env.RefreshError == "" {
			return
		}
		if admonitions {
			b.WriteString("\n> [!WARNING]\n> Couldn't refresh against the latest push; showing the last good render.\n")
		} else {
			b.WriteString("\n**⚠ Stale render**: couldn't refresh against the latest push; showing the last good render.\n")
		}
	}

	// Nothing rendered-changed (a docs/CI-only PR, or a Flux edit that nets to
	// nothing): say so plainly instead of a "+0 · 0 · −0 — 0 resources" line. Any
	// warning, failure, or image change means there's something worth showing.
	if d.Summary.Added == 0 && d.Summary.Changed == 0 && d.Summary.Removed == 0 &&
		len(d.Warnings) == 0 && len(d.Failures) == 0 && len(d.Images) == 0 {
		if admonitions {
			b.WriteString("\n> [!NOTE]\n> No rendered changes.\n")
		} else {
			b.WriteString("\nNo rendered changes.\n")
		}
		writeRefreshNote()
		writeFooter(d.HeadSHA)
		return b.String()
	}
	// The content blocks, each rendered once so a custom comment template can place
	// them individually (commentTemplateData.Sections); the default body composes
	// the same blocks in severity order — red, amber, then the neutral note — so
	// what fails the check is the first thing read: the failing box, the cautions,
	// the stale-render warning, then the headline counts (or the routine tip, which
	// already carries them, so the impact line is dropped there rather than said
	// twice), blast radius and images. The refresh note and footer are konflate's
	// own chrome and stay here.
	sec := summarySectionsFor(d, admonitions)
	appendSection := func(s string) {
		if s != "" {
			b.WriteString("\n" + s + "\n")
		}
	}
	if admonitions {
		// One red box for everything that fails the check — blockers, then render
		// failures — and one amber box for the cautions. Splitting same-severity
		// findings into separate boxes only added headlines to tell them apart;
		// each item already says what it is.
		appendSection(sectionFailing(d))
		appendSection(sec.Cautions)
	} else {
		appendSection(sec.Blocking)
		appendSection(sec.Failures)
		appendSection(sec.Cautions)
	}
	writeRefreshNote()
	if !d.Routine {
		appendSection(sec.Impact)
	}
	appendSection(sec.Routine)
	appendSection(sec.BlastRadius)
	appendSection(sec.Images)
	writeFooter(d.HeadSHA)
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
// a [!NOTE] admonition; plain keeps it bare. Always non-empty.
func sectionImpact(d *api.DiffResult, admonitions bool) string {
	impact := impactPhrase(d, true)
	if admonitions {
		return "> [!NOTE]\n> " + impact
	}
	return impact
}

// impactPhrase is the headline in words, zero terms omitted. A single-kind delta
// names the resources directly — "6 resources changed across 6 apps" — while a
// mixed one lists the signed terms and then the total: "+2 added · 3 changed ·
// −1 removed: 6 resources across 2 apps · 1 CRD". bold wraps the delta in ** so
// it leads a [!NOTE]; the routine tip embeds it plain.
func impactPhrase(d *api.DiffResult, bold bool) string {
	type term struct {
		n    int
		verb string
		sign string
	}
	var terms []term
	if d.Summary.Added > 0 {
		terms = append(terms, term{d.Summary.Added, "added", "+"})
	}
	if d.Summary.Changed > 0 {
		terms = append(terms, term{d.Summary.Changed, "changed", ""})
	}
	if d.Summary.Removed > 0 {
		terms = append(terms, term{d.Summary.Removed, "removed", "−"})
	}
	var delta string
	switch len(terms) {
	case 0:
		delta = "no rendered changes"
	case 1:
		t := terms[0]
		delta = fmt.Sprintf("%d %s %s", t.n, plural(t.n, "resource", "resources"), t.verb)
	default:
		parts := make([]string, len(terms))
		for i, t := range terms {
			parts[i] = fmt.Sprintf("%s%d %s", t.sign, t.n, t.verb)
		}
		delta = strings.Join(parts, " · ")
	}
	var b strings.Builder
	if bold && len(terms) > 0 {
		fmt.Fprintf(&b, "**%s**", delta)
	} else {
		b.WriteString(delta)
	}
	if len(terms) > 1 {
		fmt.Fprintf(&b, ": %d %s", d.Impact.Resources, plural(d.Impact.Resources, "resource", "resources"))
	}
	if d.Impact.Parents > 0 {
		fmt.Fprintf(&b, " across %d %s", d.Impact.Parents, plural(d.Impact.Parents, "app", "apps"))
	}
	if d.Impact.CRDs > 0 {
		fmt.Fprintf(&b, " · %d %s", d.Impact.CRDs, plural(d.Impact.CRDs, "CRD", "CRDs"))
	}
	if d.Truncated > 0 {
		fmt.Fprintf(&b, " · %d not shown", d.Truncated)
	}
	return b.String()
}

// sectionRoutine surfaces a routine PR — only container-image and chart-version
// changes, with nothing flagged — in one line that also carries the headline
// counts (the default body omits the impact line for a routine PR). The GitHub
// flavour uses a green [!TIP], matching the routine list pill's colour; plain
// keeps a bold line. Empty unless the diff is routine. Like the pill, it
// describes the diff's shape, not runtime safety.
func sectionRoutine(d *api.DiffResult, admonitions bool) string {
	if !d.Routine {
		return ""
	}
	scope := fmt.Sprintf("%d %s", d.Impact.Resources, plural(d.Impact.Resources, "resource", "resources"))
	if d.Impact.Parents > 0 {
		scope += fmt.Sprintf(" across %d %s", d.Impact.Parents, plural(d.Impact.Parents, "app", "apps"))
	}
	msg := "**Routine**: only container-image and chart-version changes; " + scope
	if admonitions {
		return "> [!TIP]\n> " + msg
	}
	return msg
}

// sectionBlocking renders the blocking-tier warnings — findings that fail the
// check (see checkConclusion). Red [!CAUTION], the top of the ramp, above the
// amber cautions. Empty unless a rule emitted a LevelBlocking warning (today
// only image-not-found).
// mdItem is one "- `code` — detail" line in a summary block.
type mdItem struct{ code, detail string }

// mdBlock renders a summary block: a header — a GitHub admonition (> [!ALERT],
// plus a bold line when admonitionHeader is non-empty) when admonitions, else a
// plain bold line — then one "- `code`: detail" item per entry (mdCode/mdInline
// escape the forge-controlled halves). The admonition and plain headers differ:
// the alert box already shows a titled severity icon, so a block whose items
// speak for themselves passes "" and lists them directly, while the plain form
// always carries a glyph + title because it has no box. Empty when no items.
func mdBlock(admonitions bool, alert, admonitionHeader, plainHeader string, items []mdItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	prefix := "- "
	if admonitions {
		fmt.Fprintf(&b, "> [!%s]\n", alert)
		if admonitionHeader != "" {
			fmt.Fprintf(&b, "> **%s**\n", admonitionHeader)
		}
		prefix = "> - "
	} else {
		fmt.Fprintf(&b, "**%s**\n", plainHeader)
	}
	for _, it := range items {
		fmt.Fprintf(&b, "%s`%s`: %s\n", prefix, mdCode(it.code), mdInline(it.detail))
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
	// Red [!CAUTION] — a blocker is the top of the severity ramp. The box's own
	// title does the labelling; only the plain form spells "Blocker(s)" out.
	h := fmt.Sprintf("⛔ %s", plural(len(ws), "Blocker", "Blockers"))
	return mdBlock(admonitions, "CAUTION", "", h, warningItems(ws))
}

func sectionCautions(d *api.DiffResult, admonitions bool) string {
	ws := api.WarningsByLevel(d.Warnings, api.LevelCaution)
	// Amber [!WARNING] to match the caution list pill's colour (a notch below the
	// red of a blocker or render failure); the box's own "Warning" title does the
	// labelling, so only the plain form spells "Caution(s)" out.
	h := fmt.Sprintf("⚠ %s", plural(len(ws), "Caution", "Cautions"))
	return mdBlock(admonitions, "WARNING", "", h, warningItems(ws))
}

// sectionFailing is the GitHub flavour's single red [!CAUTION] box: the
// blocking-tier warnings followed by the render failures — everything that turns
// the check red — with no headline, since the box's title and each item's text
// carry it. Empty when neither exists. The per-block Blocking / Failures
// sections stay available to custom templates.
func sectionFailing(d *api.DiffResult) string {
	items := warningItems(api.WarningsByLevel(d.Warnings, api.LevelBlocking))
	for _, f := range d.Failures {
		items = append(items, mdItem{code: f.Parent, detail: f.Message})
	}
	return mdBlock(true, "CAUTION", "", "", items)
}

func sectionFailures(d *api.DiffResult, admonitions bool) string {
	items := make([]mdItem, len(d.Failures))
	for i, f := range d.Failures {
		items[i] = mdItem{code: f.Parent, detail: f.Message}
	}
	// Red [!CAUTION] like a blocker; the plain form adds the ⛔ glyph the box drops.
	admHeader := fmt.Sprintf("%d render %s", len(d.Failures), plural(len(d.Failures), "failure", "failures"))
	plainHeader := "⛔ " + plural(len(d.Failures), "Render failure", "Render failures")
	return mdBlock(admonitions, "CAUTION", admHeader, plainHeader, items)
}

func sectionImages(d *api.DiffResult) string {
	if len(d.Images) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| image | from | to | upstream |\n|---|---|---|---|\n")
	for _, im := range d.Images {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s |\n",
			mdCode(im.Name), mdCode(shortVer(im.From)), mdCode(shortVer(im.To)), upstreamCell(im))
	}
	return strings.TrimRight(b.String(), "\n")
}

// bareRef strips the kind from a "Kind ns/name" label, leaving "ns/name".
func bareRef(label string) string {
	if _, rest, ok := strings.Cut(label, " "); ok {
		return rest
	}
	return label
}

// tagOf strips the digest from a digest-pinned version ("1.2.3@sha256:…" →
// "1.2.3"); a bare tag or bare digest passes through unchanged.
func tagOf(v string) string {
	tag, _, _ := strings.Cut(v, "@")
	return tag
}

// upstreamCell renders the image table's "upstream" column — the registry's
// verdict on the new reference as an icon plus a word: ✅ found, ❌ not found
// (the row's image raised a blocker), ❔ unverified when the head ref was never
// confirmed (verification off, a fork PR, or an indeterminate registry answer),
// or ➖ for a removal, which has nothing to verify. Always present so a reader learns the images
// were not checked rather than assuming a clean table means they were.
func upstreamCell(im api.ImageChange) string {
	switch {
	case im.To == "":
		return "➖"
	case im.Upstream == api.ImageFound:
		return "✅ found"
	case im.Upstream == api.ImageMissing:
		return "❌ not found"
	default:
		return "❔ unverified"
	}
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
		fmt.Fprintf(&b, "- `%s`: %d %s", mdCode(br.Parent), br.Transitive, plural(br.Transitive, "dependent", "dependents"))
		shown := br.Direct
		if len(shown) == 0 {
			b.WriteString("\n")
			continue
		}
		const sample = 3
		if len(shown) > sample {
			shown = shown[:sample]
		}
		// Flux dependsOn is same-kind, so the parent's kind already names the
		// dependents' kind; list them as bare ns/name (as the review does).
		names := make([]string, len(shown))
		for i, s := range shown {
			names[i] = mdInline(bareRef(s))
		}
		fmt.Fprintf(&b, " (%s", strings.Join(names, ", "))
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
// backslash before every Markdown inline metacharacter — the link/image brackets,
// the code-span backtick, the emphasis/strikethrough runs, the image bang, and
// the backslash itself (escaped first so it can't consume a following escape).
// Parens are deliberately NOT escaped: with the brackets and bang neutralised a
// bare (...) can't form a link or image, and Forgejo treats \(...\) as an inline
// KaTeX math delimiter, so escaping them mangles plain prose there (#349). Bare
// autolinked URLs are left alone: their destination is visible, so they aren't a
// spoofing vector the way [text](hidden-url) is.
var mdInlineReplacer = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", "|", `\|`,
	`\`, `\\`, "`", "\\`", "[", `\[`, "]", `\]`,
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

// shortVer trims the digest in a version — a bare "algo:hexdigest" or the digest
// half of a pinned "tag@algo:hexdigest" — to "algo:<6 hex>…" so a
// digest-pinned image doesn't sprawl across the table; tags pass through.
func shortVer(v string) string {
	if v == "" {
		return "∅"
	}
	i := strings.IndexByte(v, ':')
	if i < 0 {
		return v
	}
	if hex := v[i+1:]; len(hex) > 6 && isHex(hex) {
		return v[:i+1] + hex[:6] + "…"
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
// typically sits behind an ingress) and the configured base path.
func (s *Server) reviewURLFromRequest(r *http.Request, number int) string {
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
	return fmt.Sprintf("%s://%s%s/#/pr/%d", scheme, host, s.cfg.BasePath, number)
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
