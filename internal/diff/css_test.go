package diff

import (
	"strings"
	"testing"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
)

// TestRender_ChromaCSSThemesAreScoped guards the dual-theme fix: the light and
// dark chroma stylesheets must be scoped under distinct ancestor classes so
// they coexist instead of one overriding the other.
func TestRender_ChromaCSSThemesAreScoped(t *testing.T) {
	t.Parallel()
	res, err := Render(RenderInput{
		PRNumber: 1,
		Changes: []Change{{
			Status: "changed", Kind: "ConfigMap", Namespace: "default", Name: "cm",
			Old: map[string]any{"data": map[string]any{"k": "old"}},
			New: map[string]any{"data": map[string]any{"k": "new"}},
		}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	css := res.ChromaCSS
	if !strings.Contains(css, ".light .chroma") {
		t.Error("ChromaCSS missing light-scoped rules (.light .chroma)")
	}
	if !strings.Contains(css, ".dark .chroma") {
		t.Error("ChromaCSS missing dark-scoped rules (.dark .chroma)")
	}
	// Unscoped ".chroma" rules (the collision bug) must be gone.
	if strings.Contains(css, "\n.chroma") || strings.HasPrefix(css, ".chroma") {
		t.Error("ChromaCSS contains an unscoped .chroma rule (themes would collide)")
	}
	// chroma's theme-variant suffix must be rewritten to an ancestor: a leftover
	// ".chroma.light"/".bg.dark" needs the theme class on the wrapper itself,
	// which it never has (the theme class lives on <html>) → no highlighting.
	for _, bad := range []string{".chroma.light", ".chroma.dark", ".bg.light", ".bg.dark"} {
		if strings.Contains(css, bad) {
			t.Errorf("ChromaCSS still has compound selector %q; should be an ancestor class", bad)
		}
	}
	// A token rule must be present in the ancestor-scoped form.
	if !strings.Contains(css, ".light .chroma .") {
		t.Error("ChromaCSS missing ancestor-scoped token rules (.light .chroma .<token>)")
	}
}

// TestScopedCSS_FailsLoudWithoutModeClasses pins the resilience tripwire: when
// chroma emits no mode-compound selectors (its default since v2.27.0 gated them
// behind WithModeClasses), scopedCSS must error rather than silently return
// unscoped CSS that would let the light/dark sheets collide.
func TestScopedCSS_FailsLoudWithoutModeClasses(t *testing.T) {
	t.Parallel()
	fmtr := chromahtml.New(chromahtml.WithClasses(true)) // deliberately no WithModeClasses
	if _, err := scopedCSS(fmtr, styles.Get("github"), "light"); err == nil {
		t.Fatal("scopedCSS should error when chroma emits no mode-scoped selectors")
	}
}
