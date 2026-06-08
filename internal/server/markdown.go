package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/home-operations/konflate/internal/api"
)

// summaryMarkdown renders a PR's diff summary as a paste-ready Markdown block,
// for a CI job to post back onto the pull request. With admonitions=true it uses
// GitHub-flavoured alert blocks (> [!CAUTION] / > [!WARNING]); otherwise a plain
// bold-heading + bullet list that renders anywhere. Every forge-controlled value
// is escaped (see mdInline/mdCode) so a crafted resource name or a render error
// can't break the table or inject HTML into the comment.
func summaryMarkdown(env api.DiffEnvelope, reviewURL string, admonitions bool) string {
	var b strings.Builder
	n := env.PR.Number
	// A stable marker so a poster can find-and-edit its own comment in place
	// instead of adding a new one on every render.
	fmt.Fprintf(&b, "<!-- konflate:pr-%d -->\n", n)
	fmt.Fprintf(&b, "### konflate — rendered diff for #%d\n", n)

	writeLink := func() {
		if reviewURL != "" {
			fmt.Fprintf(&b, "\n[View the full rendered diff →](%s)\n", reviewURL)
		}
	}

	if env.Status != api.JobReady || env.Diff == nil {
		switch env.Status {
		case api.JobError:
			fmt.Fprintf(&b, "\nRender failed: %s\n", mdInline(env.Error))
		case api.JobBlocked:
			b.WriteString("\nNot rendered — fork-PR rendering is disabled for this instance.\n")
		default:
			b.WriteString("\n⏳ Still rendering — this updates once the diff is ready.\n")
		}
		writeLink()
		return b.String()
	}

	d := env.Diff
	fmt.Fprintf(&b, "\n**+%d added · %d changed · −%d removed** — %d %s",
		d.Summary.Added, d.Summary.Changed, d.Summary.Removed,
		d.Impact.Resources, plural(d.Impact.Resources, "resource", "resources"))
	if d.Impact.Parents > 0 {
		fmt.Fprintf(&b, " · %d %s", d.Impact.Parents, plural(d.Impact.Parents, "app", "apps"))
	}
	if d.Impact.CRDs > 0 {
		fmt.Fprintf(&b, " · %d %s", d.Impact.CRDs, plural(d.Impact.CRDs, "CRD", "CRDs"))
	}
	if d.Truncated > 0 {
		fmt.Fprintf(&b, " · %d not shown", d.Truncated)
	}
	b.WriteString("\n")

	if len(d.Warnings) > 0 {
		title := fmt.Sprintf("%d %s", len(d.Warnings), plural(len(d.Warnings), "caution", "cautions"))
		if admonitions {
			fmt.Fprintf(&b, "\n> [!CAUTION]\n> **%s**\n", title)
			for _, wn := range d.Warnings {
				fmt.Fprintf(&b, "> - `%s` — %s\n", mdCode(wn.Resource), mdInline(wn.Detail))
			}
		} else {
			fmt.Fprintf(&b, "\n**⚠ Cautions (%d)**\n", len(d.Warnings))
			for _, wn := range d.Warnings {
				fmt.Fprintf(&b, "- `%s` — %s\n", mdCode(wn.Resource), mdInline(wn.Detail))
			}
		}
	}

	if len(d.Failures) > 0 {
		title := fmt.Sprintf("%d render %s", len(d.Failures), plural(len(d.Failures), "failure", "failures"))
		if admonitions {
			fmt.Fprintf(&b, "\n> [!WARNING]\n> **%s**\n", title)
			for _, f := range d.Failures {
				fmt.Fprintf(&b, "> - `%s` — %s\n", mdCode(f.Parent), mdInline(f.Message))
			}
		} else {
			fmt.Fprintf(&b, "\n**⛔ Render failures (%d)**\n", len(d.Failures))
			for _, f := range d.Failures {
				fmt.Fprintf(&b, "- `%s` — %s\n", mdCode(f.Parent), mdInline(f.Message))
			}
		}
	}

	if len(d.Images) > 0 {
		b.WriteString("\n**Image changes**\n\n| image | from | to |\n|---|---|---|\n")
		for _, im := range d.Images {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n",
				mdCode(im.Name), mdCode(shortVer(im.From)), mdCode(shortVer(im.To)))
		}
	}

	writeLink()
	b.WriteString("\n<sub>konflate · advisory, not a gate</sub>\n")
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
