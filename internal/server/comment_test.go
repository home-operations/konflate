package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

func readyEnvelope() api.DiffEnvelope {
	return api.DiffEnvelope{
		Status: api.JobReady,
		PR:     api.PR{Number: 7, Title: "bump nginx", Author: "octocat"},
		Diff:   &api.DiffResult{HeadSHA: "deadbeefcafe"},
	}
}

// newCommentServer builds a minimal Server for commentBody, optionally with a
// parsed custom template (empty src = the default summary).
func newCommentServer(t *testing.T, src string) *Server {
	t.Helper()
	s := &Server{
		cfg: &config.Config{Forge: config.ForgeURI{Kind: config.ForgeGitHub}, PublicURL: "https://k.example"},
		log: discardLog(),
	}
	if src != "" {
		funcs := template.FuncMap{"md": mdInline, "mdCode": mdCode}
		tmpl, err := template.New("comment").Funcs(funcs).Option("missingkey=error").Parse(src)
		if err != nil {
			t.Fatalf("parse template: %v", err)
		}
		s.commentTmpl = tmpl
	}
	return s
}

func TestCommentBody_DefaultSummary(t *testing.T) {
	t.Parallel()
	body := newCommentServer(t, "").commentBody(readyEnvelope())
	if !strings.HasPrefix(body, konflateMarker(7)) {
		t.Errorf("default body should start with the marker: %q", body)
	}
	if !strings.Contains(body, "### konflate — summary") {
		t.Errorf("default body missing the summary heading: %q", body)
	}
}

func TestCommentBody_CustomTemplate(t *testing.T) {
	t.Parallel()
	body := newCommentServer(t, "## {{ .PR.Title }} (#{{ .PR.Number }})\n{{ .Summary }}").commentBody(readyEnvelope())
	// The marker is injected even though the template never mentions it.
	if !strings.HasPrefix(body, konflateMarker(7)) {
		t.Errorf("custom body should be prefixed with the marker: %q", body)
	}
	if !strings.Contains(body, "## bump nginx (#7)") {
		t.Errorf("custom body missing the rendered header: %q", body)
	}
	// {{ .Summary }} embeds the marker-less default, so exactly one marker total.
	if n := strings.Count(body, konflateMarker(7)); n != 1 {
		t.Errorf("expected exactly one marker, got %d: %q", n, body)
	}
	if !strings.Contains(body, "### konflate — summary") {
		t.Errorf("custom body should embed the default summary via .Summary: %q", body)
	}
}

// TestCommentBody_CustomTemplateEscapesForgeFields: a custom template that embeds
// a forge-controlled PR field raw ({{ .PR.Title }}) must not let a malicious title
// inject Markdown into konflate's own comment — the free-text fields are escaped.
func TestCommentBody_CustomTemplateEscapesForgeFields(t *testing.T) {
	t.Parallel()
	env := readyEnvelope()
	env.PR.Title = "[click](https://evil.example)"
	env.PR.Author = "a](b) ![x](c)"
	env.PR.Labels = []api.Label{{Name: "[lbl](https://evil.example)", Color: "0e8a16"}}
	body := newCommentServer(t, "T: {{ .PR.Title }}\nA: {{ .PR.Author }}\nL:{{ range .PR.Labels }} {{ .Name }}{{ end }}").
		commentBody(env)
	if strings.Contains(body, "[click](") || strings.Contains(body, "![x](") || strings.Contains(body, "[lbl](") {
		t.Errorf("a forge-controlled PR field/label must not inject a Markdown link/image:\n%s", body)
	}
	if !strings.Contains(body, `\[click\]`) || !strings.Contains(body, `\[lbl\]`) {
		t.Errorf("expected the PR title and label brackets escaped:\n%s", body)
	}
	// Escaping the copy must not mutate the caller's slice (aliasing guard).
	if env.PR.Labels[0].Name != "[lbl](https://evil.example)" {
		t.Errorf("escaping a label mutated the shared PR.Labels slice: %q", env.PR.Labels[0].Name)
	}
}

// TestCommentBody_TemplateMdFuncEscapesRawDiffText: raw forge-controlled .Diff
// text a template renders itself can be escaped with the `md` func.
func TestCommentBody_TemplateMdFuncEscapesRawDiffText(t *testing.T) {
	t.Parallel()
	env := readyEnvelope()
	env.Diff.Failures = []api.RenderFailure{{Parent: "HelmRelease ns/app", Message: "[boom](https://evil.example)"}}
	body := newCommentServer(t, "{{ range .Diff.Failures }}{{ md .Message }}{{ end }}").commentBody(env)
	if strings.Contains(body, "[boom](") {
		t.Errorf("the md func must defang a raw Diff message:\n%s", body)
	}
	if !strings.Contains(body, `\[boom\]`) {
		t.Errorf("expected the md-escaped message:\n%s", body)
	}
}

func TestCommentBody_CustomTemplateWithoutSummary(t *testing.T) {
	t.Parallel()
	body := newCommentServer(t, "a custom note for #{{ .PR.Number }}").commentBody(readyEnvelope())
	if !strings.HasPrefix(body, konflateMarker(7)+"\n") {
		t.Errorf("marker should be prepended: %q", body)
	}
	if !strings.Contains(body, "a custom note for #7") {
		t.Errorf("custom text missing: %q", body)
	}
}

func TestCommentBody_SectionsPlacedIndividually(t *testing.T) {
	t.Parallel()
	// A template that composes only two sections — the others must not leak in.
	body := newCommentServer(t, "## Cautions\n{{ .Sections.Cautions }}\n\n## Images\n{{ .Sections.Images }}").
		commentBody(sampleSummaryEnv())

	if !strings.HasPrefix(body, konflateMarker(142)) {
		t.Errorf("marker should be injected: %q", body)
	}
	if !strings.Contains(body, "[!WARNING]") || !strings.Contains(body, "Deployment web/api") {
		t.Errorf(".Sections.Cautions not rendered: %q", body)
	}
	if !strings.Contains(body, "Image changes") || !strings.Contains(body, "ghcr.io/rook/ceph") {
		t.Errorf(".Sections.Images not rendered: %q", body)
	}
	// Sections are independent: nothing the template didn't ask for appears.
	if strings.Contains(body, "added ·") {
		t.Errorf("only Cautions+Images were placed, but the Impact line leaked in: %q", body)
	}
	if strings.Contains(body, "Render failures") {
		t.Errorf("only Cautions+Images were placed, but Failures leaked in: %q", body)
	}
}

func TestCommentBody_ExecuteErrorFallsBackToDefault(t *testing.T) {
	t.Parallel()
	// .PR.Bogus parses but errors at execute → fall back to the default body.
	body := newCommentServer(t, "{{ .PR.Bogus }}").commentBody(readyEnvelope())
	if !strings.Contains(body, "### konflate — summary") {
		t.Errorf("a failing template should fall back to the default summary: %q", body)
	}
	if !strings.Contains(body, konflateMarker(7)) {
		t.Errorf("fallback body missing the marker: %q", body)
	}
}

func TestEnsureMarker(t *testing.T) {
	t.Parallel()
	m := konflateMarker(7)
	if got := ensureMarker(7, "hello"); got != m+"\nhello" {
		t.Errorf("ensureMarker should prepend the marker: %q", got)
	}
	already := "lead\n" + m + "\nbody"
	if got := ensureMarker(7, already); got != already {
		t.Errorf("ensureMarker should not duplicate an existing marker: %q", got)
	}
}

func TestNewCommentTemplate(t *testing.T) {
	t.Parallel()
	t.Run("nil when unset", func(t *testing.T) {
		t.Parallel()
		if tmpl := newCommentTemplate(&config.Config{}, discardLog()); tmpl != nil {
			t.Error("want nil when no template file is configured")
		}
	})
	t.Run("parses a file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "c.gotmpl")
		if err := os.WriteFile(path, []byte("hi #{{ .PR.Number }}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if newCommentTemplate(&config.Config{PRCommentTemplateFile: path}, discardLog()) == nil {
			t.Error("want a parsed template")
		}
	})
	t.Run("nil on a missing file", func(t *testing.T) {
		t.Parallel()
		if newCommentTemplate(&config.Config{PRCommentTemplateFile: "/nope/x.gotmpl"}, discardLog()) != nil {
			t.Error("want nil when the file can't be read")
		}
	})
	t.Run("nil on a parse error", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "bad.gotmpl")
		if err := os.WriteFile(path, []byte("{{ .PR.Number "), 0o600); err != nil { // unclosed action
			t.Fatal(err)
		}
		if newCommentTemplate(&config.Config{PRCommentTemplateFile: path}, discardLog()) != nil {
			t.Error("want nil on a parse error")
		}
	})
}
