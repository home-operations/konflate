package diff

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/pmezard/go-difflib/difflib"
	"sigs.k8s.io/yaml"

	"github.com/home-operations/konflate/internal/api"
)

// RenderInput is everything the renderer needs to produce a DiffResult. The
// engine builds it from the two flate renders (merge-base and head): the paired
// changes, the image diff, and any render failures.
type RenderInput struct {
	PRNumber int
	HeadSHA  string
	Changes  []Change
	Images   []api.ImageChange
	Failures []api.RenderFailure
	// MaxResources caps how many changes are fully rendered into per-resource
	// rows (the dominant memory/payload cost). Excess changes are dropped from
	// the rendered set and counted in DiffResult.Truncated; the summary and
	// impact still reflect the full change set. <=0 means no cap.
	MaxResources int
}

// Row and cell kinds, matching the api string-literal unions.
const (
	kindCtx   = "ctx"
	kindAdd   = "add"
	kindDel   = "del"
	kindBlank = "blank"
)

// Render turns the paired changes into the full api.DiffResult: per-resource
// unified + side-by-side rows with YAML syntax highlighting, the navigation
// tree, and the review signals (impact, images, failures, danger-lint).
func Render(in RenderInput) (api.DiffResult, error) {
	b, err := sharedHighlighter()
	if err != nil {
		return api.DiffResult{}, err
	}
	hl := b.hl

	// flate classifies a change by typed inequality (reflect.DeepEqual over the
	// normalized values), so a difference that is only a type — e.g. replicas: 3
	// (int) vs 3.0 (float64) — arrives as "changed" yet marshals to identical YAML
	// through sigs.k8s.io/yaml (both via JSON → "3"). Rendered, that's zero diff
	// rows: an empty panel, still counted in Summary.Changed and listed in the
	// tree as +0/−0. Marshal each side once here and drop those no-ops before
	// anything counts or renders them, so a resource appears only when it has a
	// real textual difference. (Added/removed always have one populated side, so
	// only "changed" can collapse to a no-op.)
	kept := make([]marshaledChange, 0, len(in.Changes))
	for _, c := range in.Changes {
		oldYAML, err := marshalYAML(c.Old)
		if err != nil {
			return api.DiffResult{}, err
		}
		newYAML, err := marshalYAML(c.New)
		if err != nil {
			return api.DiffResult{}, err
		}
		if c.Status != statusAdded && c.Status != statusRemoved && oldYAML == newYAML {
			continue
		}
		kept = append(kept, marshaledChange{change: c, oldYAML: oldYAML, newYAML: newYAML})
	}
	changes := make([]Change, len(kept))
	for i := range kept {
		changes[i] = kept[i].change
	}

	out := api.DiffResult{
		PRNumber:  in.PRNumber,
		HeadSHA:   in.HeadSHA,
		ChromaCSS: b.css,
		Impact:    Summarize(changes),
		Images:    in.Images,
		Failures:  in.Failures,
		Warnings:  Lint(changes, in.Images),
	}

	// Summary reflects the true totals across every (real) change, even when the
	// per-resource render below is capped — so the topbar counts and the impact
	// banner agree on the real blast radius regardless of truncation.
	for _, c := range changes {
		switch c.Status {
		case statusAdded:
			out.Summary.Added++
		case statusRemoved:
			out.Summary.Removed++
		default:
			out.Summary.Changed++
		}
	}

	// Cap the per-resource render — the dominant memory and payload cost (each
	// resource carries pre-highlighted unified + side-by-side rows). A sweeping
	// or pathological PR is truncated to MaxResources rendered diffs; the counts
	// above already reflect the full set, and Truncated tells the UI the review
	// is partial.
	rendered := kept
	if in.MaxResources > 0 && len(rendered) > in.MaxResources {
		out.Truncated = len(rendered) - in.MaxResources
		rendered = rendered[:in.MaxResources]
	}
	for i, mc := range rendered {
		out.Resources = append(out.Resources, buildResource(fmt.Sprintf("r%d", i), mc, hl))
	}
	out.Tree = buildTree(out.Resources)
	return out, nil
}

// marshaledChange is a Change with both sides already rendered to YAML — done
// once in Render so the type-only no-op filter and buildResource share the work
// instead of marshaling twice.
type marshaledChange struct {
	change           Change
	oldYAML, newYAML string
}

// buildResource diffs one change's already-marshaled old→new YAML and
// pre-renders both views. The sides are marshaled in Render, so this is pure
// string work and cannot fail.
func buildResource(id string, mc marshaledChange, hl *highlighter) api.DiffResource {
	c, oldYAML, newYAML := mc.change, mc.oldYAML, mc.newYAML

	a := difflib.SplitLines(oldYAML)
	b := difflib.SplitLines(newYAML)
	ah := hl.lines(a)
	bh := hl.lines(b)
	groups := difflib.NewMatcher(a, b).GetGroupedOpCodes(3)
	markIntraline(a, b, groups, hl, ah, bh)

	name := c.Name
	if c.Namespace != "" {
		name = c.Namespace + "/" + c.Name
	}
	res := api.DiffResource{
		ID:      id,
		Title:   c.Kind + " " + name,
		Kind:    c.Kind,
		Name:    name,
		Parent:  c.Parent,
		Status:  c.Status,
		Unified: unifiedRows(ah, bh, groups),
		Side:    sideRows(ah, bh, groups),
	}
	for _, r := range res.Unified {
		switch r.Kind {
		case kindAdd:
			res.Add++
		case kindDel:
			res.Del++
		}
	}
	return res
}

// gap is one collapsed run of unchanged lines — the context GetGroupedOpCodes
// trims before, between, and after the hunks — tagged with a positional fold id
// shared by both views so a single expand reveals the run in each.
type gap struct {
	id                 string
	oldStart, newStart int
	count              int
}

// foldGaps returns the trimmed unchanged runs indexed by the group they
// precede: index 0 is the run before the first hunk, index gi the run between
// groups gi-1 and gi, and index len(groups) the run after the last. These are
// the lines GetGroupedOpCodes drops; emitting them as collapsed rows behind an
// expander lets the whole file be revealed in place. Empty runs stay zero-value
// (count 0) so the row builders can index by position and skip them.
func foldGaps(groups [][]difflib.OpCode, nOld int) []gap {
	gaps := make([]gap, len(groups)+1)
	set := func(pos, oldStart, oldEnd, newStart int) {
		if oldEnd > oldStart {
			gaps[pos] = gap{id: fmt.Sprintf("g%d", pos), oldStart: oldStart, newStart: newStart, count: oldEnd - oldStart}
		}
	}
	for gi, group := range groups {
		if gi == 0 {
			set(0, 0, group[0].I1, 0)
			continue
		}
		prev := groups[gi-1][len(groups[gi-1])-1]
		set(gi, prev.I2, group[0].I1, prev.J2)
	}
	if n := len(groups); n > 0 {
		last := groups[n-1][len(groups[n-1])-1]
		set(n, last.I2, nOld, last.J2)
	}
	return gaps
}

// unifiedRows renders grouped opcodes as the single-column unified view. The
// unchanged runs foldGaps reports are emitted as collapsed context rows behind
// an expander row, so the rest of the file can be revealed in place.
func unifiedRows(ah, bh []string, groups [][]difflib.OpCode) []api.UnifiedRow {
	var rows []api.UnifiedRow
	gaps := foldGaps(groups, len(ah))
	fold := func(pos int) {
		g := gaps[pos]
		if g.count == 0 {
			return
		}
		rows = append(rows, api.UnifiedRow{Hunk: true, Fold: g.id, Count: g.count})
		for k := range g.count {
			rows = append(rows, api.UnifiedRow{Folded: true, Fold: g.id, Kind: kindCtx,
				OldNo: g.oldStart + k + 1, NewNo: g.newStart + k + 1, HTML: ah[g.oldStart+k]})
		}
	}
	for gi, group := range groups {
		fold(gi)
		for _, op := range group {
			switch op.Tag {
			case 'e':
				for k := range op.I2 - op.I1 {
					rows = append(rows, api.UnifiedRow{Kind: kindCtx, OldNo: op.I1 + k + 1, NewNo: op.J1 + k + 1, HTML: ah[op.I1+k]})
				}
			case 'd', 'r':
				for k := range op.I2 - op.I1 {
					rows = append(rows, api.UnifiedRow{Kind: kindDel, OldNo: op.I1 + k + 1, HTML: ah[op.I1+k]})
				}
				if op.Tag == 'r' {
					for k := range op.J2 - op.J1 {
						rows = append(rows, api.UnifiedRow{Kind: kindAdd, NewNo: op.J1 + k + 1, HTML: bh[op.J1+k]})
					}
				}
			case 'i':
				for k := range op.J2 - op.J1 {
					rows = append(rows, api.UnifiedRow{Kind: kindAdd, NewNo: op.J1 + k + 1, HTML: bh[op.J1+k]})
				}
			}
		}
	}
	fold(len(groups))
	return rows
}

// sideRows renders grouped opcodes as the two-column side-by-side view, with the
// same folded-context expanders as unifiedRows (sharing fold ids so one expand
// reveals both views).
func sideRows(ah, bh []string, groups [][]difflib.OpCode) []api.SideRow {
	var rows []api.SideRow
	gaps := foldGaps(groups, len(ah))
	fold := func(pos int) {
		g := gaps[pos]
		if g.count == 0 {
			return
		}
		rows = append(rows, api.SideRow{Hunk: true, Fold: g.id, Count: g.count})
		for k := range g.count {
			rows = append(rows, api.SideRow{Folded: true, Fold: g.id,
				Left:  api.SideCell{Kind: kindCtx, No: g.oldStart + k + 1, HTML: ah[g.oldStart+k]},
				Right: api.SideCell{Kind: kindCtx, No: g.newStart + k + 1, HTML: bh[g.newStart+k]},
			})
		}
	}
	for gi, group := range groups {
		fold(gi)
		for _, op := range group {
			switch op.Tag {
			case 'e':
				for k := range op.I2 - op.I1 {
					rows = append(rows, api.SideRow{
						Left:  api.SideCell{Kind: kindCtx, No: op.I1 + k + 1, HTML: ah[op.I1+k]},
						Right: api.SideCell{Kind: kindCtx, No: op.J1 + k + 1, HTML: bh[op.J1+k]},
					})
				}
			case 'd':
				for k := range op.I2 - op.I1 {
					rows = append(rows, api.SideRow{
						Left:  api.SideCell{Kind: kindDel, No: op.I1 + k + 1, HTML: ah[op.I1+k]},
						Right: api.SideCell{Kind: kindBlank},
					})
				}
			case 'i':
				for k := range op.J2 - op.J1 {
					rows = append(rows, api.SideRow{
						Left:  api.SideCell{Kind: kindBlank},
						Right: api.SideCell{Kind: kindAdd, No: op.J1 + k + 1, HTML: bh[op.J1+k]},
					})
				}
			case 'r':
				dn, an := op.I2-op.I1, op.J2-op.J1
				for k := range max(dn, an) {
					l, r := api.SideCell{Kind: kindBlank}, api.SideCell{Kind: kindBlank}
					if k < dn {
						l = api.SideCell{Kind: kindDel, No: op.I1 + k + 1, HTML: ah[op.I1+k]}
					}
					if k < an {
						r = api.SideCell{Kind: kindAdd, No: op.J1 + k + 1, HTML: bh[op.J1+k]}
					}
					rows = append(rows, api.SideRow{Left: l, Right: r})
				}
			}
		}
	}
	fold(len(groups))
	return rows
}

// markIntraline rewrites replace-aligned line pairs in ah/bh to add word-level
// highlight spans around the runes that actually changed, so a one-character
// edit reads as a one-character highlight instead of a whole-line tint.
func markIntraline(a, b []string, groups [][]difflib.OpCode, hl *highlighter, ah, bh []string) {
	for _, group := range groups {
		for _, op := range group {
			if op.Tag != 'r' {
				continue
			}
			for k := range min(op.I2-op.I1, op.J2-op.J1) {
				ai, bj := op.I1+k, op.J1+k
				aLo, aHi, bLo, bHi := runeDiffRange(strings.TrimRight(a[ai], "\n"), strings.TrimRight(b[bj], "\n"))
				if aHi > aLo {
					ah[ai] = hl.emit(a[ai], aLo, aHi)
				}
				if bHi > bLo {
					bh[bj] = hl.emit(b[bj], bLo, bHi)
				}
			}
		}
	}
}

// runeDiffRange returns the per-side changed rune ranges of two differing lines
// by trimming the common prefix and suffix. It returns empty ranges when the
// lines share no common affix (a total change — not worth a word highlight).
func runeDiffRange(a, b string) (aLo, aHi, bLo, bHi int) {
	ar, br := []rune(a), []rune(b)
	p := 0
	for p < len(ar) && p < len(br) && ar[p] == br[p] {
		p++
	}
	s := 0
	for s < len(ar)-p && s < len(br)-p && ar[len(ar)-1-s] == br[len(br)-1-s] {
		s++
	}
	if p == 0 && s == 0 {
		return 0, 0, 0, 0
	}
	return p, len(ar) - s, p, len(br) - s
}

// buildTree groups resources (kept in pair order: parent → kind → name) into
// the sidebar hierarchy.
func buildTree(res []api.DiffResource) []api.DiffTreeParent {
	var tree []api.DiffTreeParent
	for _, r := range res {
		if len(tree) == 0 || tree[len(tree)-1].Label != r.Parent {
			tree = append(tree, api.DiffTreeParent{Label: r.Parent})
		}
		tp := &tree[len(tree)-1]
		if len(tp.Kinds) == 0 || tp.Kinds[len(tp.Kinds)-1].Kind != r.Kind {
			tp.Kinds = append(tp.Kinds, api.DiffTreeKind{Kind: r.Kind})
		}
		tk := &tp.Kinds[len(tp.Kinds)-1]
		tk.Items = append(tk.Items, api.DiffTreeItem{ID: r.ID, Name: r.Name, Status: r.Status, Add: r.Add, Del: r.Del})
	}
	return tree
}

// marshalYAML renders a manifest to deterministic (sorted-key) YAML. A nil
// manifest (added or removed side) yields an empty string.
func marshalYAML(m map[string]any) (string, error) {
	if m == nil {
		return "", nil
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	return string(b), nil
}

// highlighter renders single YAML lines to class-based chroma HTML. The
// light (github) + dark (github-dark) stylesheets, scoped under
// .chroma.light / .chroma.dark, are emitted once into DiffResult.ChromaCSS;
// the spans are theme-agnostic.
type highlighter struct {
	lexer chroma.Lexer
	style *chroma.Style
	fmtr  *chromahtml.Formatter
}

// built is the shared, immutable highlighter plus its dual-theme stylesheet.
type built struct {
	hl  *highlighter
	css string
}

// sharedHighlighter builds the YAML highlighter and its (static) dual-theme CSS
// exactly once, then hands the same instance to every render. The lexer and
// formatter are reused across concurrent renders — chroma's Tokenise/Format are
// safe for concurrent use, and the lexer's lazy rule compilation is warmed here
// under the Once so it never races — and the CSS string is shared rather than
// regenerated and re-stored in every DiffResult.
var sharedHighlighter = sync.OnceValues(buildHighlighter)

func buildHighlighter() (*built, error) {
	lexer := lexers.Get("yaml")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	light := styles.Get("github")
	if light == nil {
		light = styles.Fallback
	}
	dark := styles.Get("github-dark")
	if dark == nil {
		dark = light
	}
	fmtr := chromahtml.New(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true))
	// The github / github-dark styles are theme variants: chroma emits selectors
	// like ".chroma.light .err" / ".chroma.dark .err" (the theme as a second
	// class on the wrapper). We rewrite that suffix into an ancestor class so the
	// sheet keys off a ".light"/".dark" class on a page ancestor (we put it on
	// <html>) with ".chroma" on the diff table — letting one class toggle switch
	// the highlight theme.
	lightCSS, err := scopedCSS(fmtr, light, "light")
	if err != nil {
		return nil, fmt.Errorf("chroma css (light): %w", err)
	}
	darkCSS, err := scopedCSS(fmtr, dark, "dark")
	if err != nil {
		return nil, fmt.Errorf("chroma css (dark): %w", err)
	}
	h := &highlighter{lexer: lexer, style: light, fmtr: fmtr}
	_ = h.line("warm the lexer's lazy rule compilation once, single-threaded")
	return &built{hl: h, css: lightCSS + "\n" + darkCSS}, nil
}

// scopedCSS renders style's class-based stylesheet and moves chroma's
// theme-variant suffix (".chroma.light", ".bg.dark") to an ancestor class
// (".light .chroma", ".dark .bg"), so the sheet keys off a theme class on a
// page ancestor with ".chroma" on the diff table.
func scopedCSS(fmtr *chromahtml.Formatter, style *chroma.Style, theme string) (string, error) {
	var raw bytes.Buffer
	if err := fmtr.WriteCSS(&raw, style); err != nil {
		return "", err
	}
	css := raw.String()
	css = strings.ReplaceAll(css, ".chroma."+theme, "."+theme+" .chroma")
	css = strings.ReplaceAll(css, ".bg."+theme, "."+theme+" .bg")
	return css, nil
}

func (h *highlighter) lines(src []string) []string {
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = h.line(s)
	}
	return out
}

func (h *highlighter) line(s string) string {
	s = strings.TrimRight(s, "\n")
	it, err := h.lexer.Tokenise(nil, s)
	if err != nil {
		return template.HTMLEscapeString(s)
	}
	var b strings.Builder
	if err := h.fmtr.Format(&b, h.style, it); err != nil {
		return template.HTMLEscapeString(s)
	}
	return b.String()
}

// emit renders one YAML line to class-based highlighted HTML, walking chroma's
// token stream directly (rather than its formatter) so a word-level highlight
// can be spliced mid-token: the rune range [lo,hi) is wrapped in
// <span class="wd">. Token classes mirror chroma's WithClasses output
// (StandardTypes), so they match the same embedded stylesheet line() produces.
func (h *highlighter) emit(s string, lo, hi int) string {
	s = strings.TrimRight(s, "\n")
	it, err := h.lexer.Tokenise(nil, s)
	if err != nil {
		return template.HTMLEscapeString(s)
	}
	esc := func(r []rune) string { return template.HTMLEscapeString(string(r)) }
	var b strings.Builder
	pos := 0
	for t := it(); t != chroma.EOF; t = it() {
		rs := []rune(t.Value)
		n := len(rs)
		class := classFor(t.Type)
		if class != "" {
			b.WriteString(`<span class="`)
			b.WriteString(class)
			b.WriteString(`">`)
		}
		// Wrap [lo,hi)'s overlap with this token (clamped to the token's rune
		// span) in .wd; an empty overlap leaves the token plain.
		if lo2, hi2 := max(0, min(n, lo-pos)), max(0, min(n, hi-pos)); lo2 < hi2 {
			b.WriteString(esc(rs[:lo2]))
			b.WriteString(`<span class="wd">`)
			b.WriteString(esc(rs[lo2:hi2]))
			b.WriteString(`</span>`)
			b.WriteString(esc(rs[hi2:]))
		} else {
			b.WriteString(esc(rs))
		}
		if class != "" {
			b.WriteString(`</span>`)
		}
		pos += n
	}
	return b.String()
}

// classFor maps a chroma token type to its short CSS class, mirroring the
// formatter's StandardTypes lookup (with category fallback) so the hand-emitted
// spans align with chroma's generated stylesheet.
func classFor(tt chroma.TokenType) string {
	if c, ok := chroma.StandardTypes[tt]; ok {
		return c
	}
	if c, ok := chroma.StandardTypes[tt.SubCategory()]; ok {
		return c
	}
	if c, ok := chroma.StandardTypes[tt.Category()]; ok {
		return c
	}
	return ""
}
