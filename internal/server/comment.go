package server

import (
	"log/slog"
	"os"
	"strings"
	"text/template"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// commentTemplateData is the context a custom PR-comment template renders against
// (KONFLATE_PR_COMMENT_TEMPLATE_FILE). The fields are a stable contract; the
// `api` types are the same ones the JSON API exposes.
type commentTemplateData struct {
	// PR is the pull request: .PR.Number, .PR.Title, .PR.Author, .PR.HeadRef,
	// .PR.HeadSHA, .PR.BaseRef, .PR.URL, ... The free-text fields (Title, Author,
	// HeadRef, BaseRef, and each Labels[].Name/.Color) are Markdown-escaped so a
	// forge-controlled value embedded raw in the template can't inject into
	// konflate's own comment; URL/HeadSHA/Number/State are structural and pass
	// through unchanged (URL stays usable as a link target).
	PR api.PR
	// Diff is the rendered diff: .Diff.Impact.Resources, .Diff.Warnings,
	// .Diff.Images, .Diff.Failures, ... Comments render on a successful render, so
	// this is populated. Its text fields (warning details, failure messages, image
	// refs) are forge-controlled and raw — escape them with the `md`/`mdCode`
	// template funcs when rendering them directly, or use the pre-escaped
	// .Summary / .Sections instead.
	Diff *api.DiffResult
	// ReviewURL is konflate's review link for the PR, or "" when KONFLATE_PUBLIC_URL
	// is unset.
	ReviewURL string
	// Summary is konflate's built-in summary body (without the marker), so a custom
	// template can wrap or extend the default with `{{ .Summary }}`.
	Summary string
	// Sections are the summary's individual blocks (.Sections.Impact, .Cautions,
	// .Failures, .Images, .BlastRadius) as rendered Markdown, to place à la carte
	// instead of the whole `{{ .Summary }}`. Empty fields when a block is absent.
	Sections summarySections
}

// newCommentTemplate parses the operator's PR-comment template file, or returns
// nil (use the built-in summary) when none is configured. A missing or invalid
// template is logged and disabled rather than failing startup — konflate falls
// back to the default body, so a typo doesn't stop it commenting.
func newCommentTemplate(cfg *config.Config, log *slog.Logger) *template.Template {
	path := cfg.PRCommentTemplateFile
	if path == "" {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		log.Warn("cannot read KONFLATE_PR_COMMENT_TEMPLATE_FILE; using the default comment body",
			"path", path, "error", err)
		return nil
	}
	// md / mdCode let a template author Markdown-escape any raw forge-controlled
	// field they render themselves (e.g. iterating .Diff.Warnings) — text/template
	// does no escaping, and this comment is posted under konflate's own identity.
	// The scalar .PR fields and the pre-rendered .Summary/.Sections are already safe.
	funcs := template.FuncMap{"md": mdInline, "mdCode": mdCode}
	t, err := template.New("comment").Funcs(funcs).Option("missingkey=error").Parse(string(src))
	if err != nil {
		log.Warn("invalid KONFLATE_PR_COMMENT_TEMPLATE_FILE; using the default comment body",
			"path", path, "error", err)
		return nil
	}
	return t
}

// commentBody builds the PR-comment body for env: the operator's custom template
// when configured, otherwise konflate's default summary. The konflate marker is
// always present (so write-back edits the comment in place) — injected here, so a
// custom template needn't include it. A template that errors at render falls back
// to the default body so a render still leaves a useful comment.
func (s *Server) commentBody(env api.DiffEnvelope) string {
	reviewURL := s.reviewURL(env.PR.Number)
	admonitions := s.cfg.Forge.Kind == config.ForgeGitHub
	defaultBody := func() string { return summaryMarkdown(env, reviewURL, admonitions) }
	if s.commentTmpl == nil {
		return defaultBody()
	}
	// The comment is authored under konflate's own identity, so forge-controlled
	// free-text PR fields must be Markdown-safe even when a custom template embeds
	// them raw (text/template does not escape): a fork PR titled "[x](evil)" would
	// otherwise inject a link. Escape them on a copy (the api.PR contract is
	// preserved; Number/URL/HeadSHA stay usable as-is — URL is a link target).
	pr := env.PR
	pr.Title = mdInline(pr.Title)
	pr.Author = mdInline(pr.Author)
	pr.HeadRef = mdInline(pr.HeadRef)
	pr.BaseRef = mdInline(pr.BaseRef)
	// Labels are forge-controlled too. The struct copy above aliases the Labels
	// slice, so deep-copy it before escaping each name/color — otherwise this would
	// mutate the shared api.PR the rest of the request still reads.
	if len(pr.Labels) > 0 {
		labels := make([]api.Label, len(pr.Labels))
		copy(labels, pr.Labels)
		for i := range labels {
			labels[i].Name = mdInline(labels[i].Name)
			labels[i].Color = mdInline(labels[i].Color)
		}
		pr.Labels = labels
	}
	data := commentTemplateData{
		PR:        pr,
		Diff:      env.Diff,
		ReviewURL: reviewURL,
		Summary:   summaryMarkdownBody(env, reviewURL, admonitions),
		Sections:  summarySectionsFor(env.Diff, admonitions),
	}
	var b strings.Builder
	if err := s.commentTmpl.Execute(&b, data); err != nil {
		s.log.Warn("custom PR comment template failed; using the default comment body",
			"pr", env.PR.Number, "error", err)
		return defaultBody()
	}
	return ensureMarker(env.PR.Number, b.String())
}

// ensureMarker guarantees the konflate marker is in body so comment write-back can
// find and edit the comment; a custom template needn't include it. The marker is a
// hidden HTML comment, so prepending it is invisible in the rendered comment.
func ensureMarker(number int, body string) string {
	marker := konflateMarker(number)
	if strings.Contains(body, marker) {
		return body
	}
	return marker + "\n" + body
}
