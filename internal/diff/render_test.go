package diff

import (
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/home-operations/konflate/internal/api"
)

func doc(t *testing.T, y string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(y), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestRender_Changed(t *testing.T) {
	t.Parallel()
	old := doc(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\n  namespace: apps\ndata:\n  level: \"2\"\n")
	neu := doc(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\n  namespace: apps\ndata:\n  level: \"3\"\n")

	res, err := Render(RenderInput{
		PRNumber: 42, HeadSHA: "abc123",
		Changes: []Change{{Status: "changed", Kind: "ConfigMap", Namespace: "apps", Name: "web", Parent: "HelmRelease apps/web", Old: old, New: neu}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if res.PRNumber != 42 || res.HeadSHA != "abc123" {
		t.Errorf("PR/SHA = %d/%q", res.PRNumber, res.HeadSHA)
	}
	if res.Summary.Changed != 1 || res.Summary.Added != 0 || res.Summary.Removed != 0 {
		t.Errorf("summary = %+v, want 1 changed", res.Summary)
	}
	if res.Impact.Resources != 1 {
		t.Errorf("impact.Resources = %d, want 1", res.Impact.Resources)
	}
	if len(res.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(res.Resources))
	}
	r := res.Resources[0]
	if r.Title != "ConfigMap apps/web" || r.Parent != "HelmRelease apps/web" {
		t.Errorf("resource title/parent = %q / %q", r.Title, r.Parent)
	}
	if len(res.Tree) != 1 || res.Tree[0].Label != "HelmRelease apps/web" ||
		len(res.Tree[0].Kinds) != 1 || res.Tree[0].Kinds[0].Kind != "ConfigMap" ||
		len(res.Tree[0].Kinds[0].Items) != 1 || res.Tree[0].Kinds[0].Items[0].ID != r.ID {
		t.Errorf("tree malformed: %+v", res.Tree)
	}
	if !unifiedKinds(r.Unified)["del"] || !unifiedKinds(r.Unified)["add"] {
		t.Errorf("unified rows missing del/add: %+v", r.Unified)
	}
	if r.Add == 0 || r.Del == 0 {
		t.Errorf("add/del counts = +%d/-%d, want both > 0", r.Add, r.Del)
	}
	if len(r.Side) == 0 {
		t.Error("side rows empty")
	}
	if !strings.Contains(res.ChromaCSS, ".light .chroma") || !strings.Contains(res.ChromaCSS, ".dark .chroma") {
		t.Error("ChromaCSS missing light/dark ancestor scopes")
	}
	if !unifiedHasSpan(r.Unified) {
		t.Error("no chroma token spans in highlighted rows")
	}
	// The only change is "2"→"3"; it should be a word-level highlight, not a
	// whole-line tint.
	if !unifiedHasHTML(r.Unified, `class="wd"`) {
		t.Error("expected a word-level (.wd) highlight on the changed character")
	}
}

func TestRender_FoldsContext(t *testing.T) {
	t.Parallel()
	// A single change surrounded by many unchanged lines: context beyond 3 lines
	// must be folded behind expanders rather than dropped.
	var oldB, newB strings.Builder
	oldB.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n")
	newB.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n")
	// Lines present only in the base make the two files different lengths, so a
	// folded gap's old-file offset can exceed the new file's length — each side
	// of the split view must index with its own offset (regression: a panic when
	// the new side was indexed with the old offset).
	for i := range 12 {
		fmt.Fprintf(&oldB, "  gone%02d: \"x\"\n", i)
	}
	for i := range 20 {
		fmt.Fprintf(&oldB, "  k%02d: \"keep\"\n", i)
		val := "keep"
		if i == 10 {
			val = "CHANGED"
		}
		fmt.Fprintf(&newB, "  k%02d: %q\n", i, val)
	}

	res, err := Render(RenderInput{
		Changes: []Change{{Status: "changed", Kind: "ConfigMap", Name: "cfg", Parent: "HelmRelease x/y", Old: doc(t, oldB.String()), New: doc(t, newB.String())}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	r := res.Resources[0]

	var expanders, foldedRows int
	folds := map[string]bool{}
	for _, row := range r.Unified {
		switch {
		case row.Hunk:
			if row.Fold == "" || row.Count == 0 {
				t.Errorf("expander row missing fold id/count: %+v", row)
			}
			expanders++
			folds[row.Fold] = true
		case row.Folded:
			if row.Fold == "" {
				t.Errorf("folded row missing fold id: %+v", row)
			}
			foldedRows++
		}
	}
	if expanders == 0 || foldedRows == 0 {
		t.Fatalf("expected folded context behind expanders, got %d expanders / %d folded rows", expanders, foldedRows)
	}
	// Side view shares the same fold ids so one expand reveals both.
	for _, row := range r.Side {
		if row.Hunk && !folds[row.Fold] {
			t.Errorf("side expander fold %q not shared with unified view", row.Fold)
		}
	}
}

func TestRender_AddedAndRemoved(t *testing.T) {
	t.Parallel()
	added := doc(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: new\n  namespace: apps\ndata:\n  k: v\n")
	removed := doc(t, "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: db\n  namespace: data\n")

	res, err := Render(RenderInput{
		Changes: []Change{
			{Status: "added", Kind: "ConfigMap", Namespace: "apps", Name: "new", Parent: "HelmRelease apps/x", New: added},
			{Status: "removed", Kind: "StatefulSet", Namespace: "data", Name: "db", Parent: "HelmRelease data/db", Old: removed},
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Summary.Added != 1 || res.Summary.Removed != 1 || res.Summary.Changed != 0 {
		t.Errorf("summary = %+v, want 1 added / 1 removed", res.Summary)
	}
	for _, r := range res.Resources {
		ks := unifiedKinds(r.Unified)
		switch r.Status {
		case "added":
			if ks["del"] || !ks["add"] {
				t.Errorf("added %s should have only add rows: %+v", r.Title, ks)
			}
		case "removed":
			if ks["add"] || !ks["del"] {
				t.Errorf("removed %s should have only del rows: %+v", r.Title, ks)
			}
		}
	}
	if !hasWarning(res.Warnings, "removed-statefulset") {
		t.Errorf("expected removed-statefulset warning, got %+v", res.Warnings)
	}
}

// TestRender_EscapesHTMLInValues verifies a manifest value carrying HTML markup
// is escaped in the rendered diff, never emitted as a live tag. A fork PR is
// untrusted content, so a crafted value (a <script> in a ConfigMap) must not
// reach the browser as markup — defense-in-depth behind the strict CSP. Only
// chroma's own <span> markup is allowed in row HTML.
func TestRender_EscapesHTMLInValues(t *testing.T) {
	t.Parallel()
	const payload = "<script>alert(1)</script>"
	old := doc(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: \"1\"\n")
	neu := doc(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: \"1\"\n  evil: '<script>alert(1)</script>'\n")

	res, err := Render(RenderInput{
		PRNumber: 1,
		Changes:  []Change{{Status: "changed", Kind: "ConfigMap", Name: "x", Parent: "HelmRelease x/y", Old: old, New: neu}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	r := res.Resources[0]
	// The security property: the raw tag never appears in any rendered row.
	if unifiedHasHTML(r.Unified, "<script") || sideHasHTML(r.Side, "<script") {
		t.Errorf("raw <script> tag leaked unescaped into the rendered diff; payload=%q", payload)
	}
	// And the value still renders, escaped, so the reviewer sees the change.
	if !unifiedHasHTML(r.Unified, "&lt;") {
		t.Error("expected the payload's '<' to be HTML-escaped (&lt;) in the rendered rows")
	}
}

// TestRender_TruncatesAtCap verifies the per-resource render is capped at
// MaxResources while the summary and impact still report the true totals, and
// that the no-cap path renders everything.
func TestRender_TruncatesAtCap(t *testing.T) {
	t.Parallel()
	mk := func(n int) Change {
		y := doc(t, fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c%d\n", n))
		return Change{Status: "added", Kind: "ConfigMap", Name: fmt.Sprintf("c%d", n), Parent: "HelmRelease x/y", New: y}
	}
	changes := make([]Change, 5)
	for i := range changes {
		changes[i] = mk(i)
	}

	res, err := Render(RenderInput{PRNumber: 1, Changes: changes, MaxResources: 2})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(res.Resources) != 2 {
		t.Errorf("rendered %d resources, want 2 (capped)", len(res.Resources))
	}
	if res.Truncated != 3 {
		t.Errorf("Truncated = %d, want 3", res.Truncated)
	}
	// Summary and impact reflect the TRUE total (all 5), not the capped render.
	if res.Summary.Added != 5 {
		t.Errorf("Summary.Added = %d, want 5 (true total despite truncation)", res.Summary.Added)
	}
	if res.Impact.Resources != 5 {
		t.Errorf("Impact.Resources = %d, want 5 (true total)", res.Impact.Resources)
	}

	full, err := Render(RenderInput{PRNumber: 1, Changes: changes})
	if err != nil {
		t.Fatalf("Render (no cap): %v", err)
	}
	if full.Truncated != 0 || len(full.Resources) != 5 {
		t.Errorf("no-cap render: Truncated=%d resources=%d, want 0/5", full.Truncated, len(full.Resources))
	}
}

func unifiedKinds(rows []api.UnifiedRow) map[string]bool {
	m := map[string]bool{}
	for _, r := range rows {
		if r.Kind != "" {
			m[r.Kind] = true
		}
	}
	return m
}

func unifiedHasSpan(rows []api.UnifiedRow) bool {
	return unifiedHasHTML(rows, `<span class="`)
}

func unifiedHasHTML(rows []api.UnifiedRow, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.HTML, substr) {
			return true
		}
	}
	return false
}

func sideHasHTML(rows []api.SideRow, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.Left.HTML, substr) || strings.Contains(r.Right.HTML, substr) {
			return true
		}
	}
	return false
}

func hasWarning(ws []api.Warning, rule string) bool {
	for _, w := range ws {
		if w.Rule == rule {
			return true
		}
	}
	return false
}
