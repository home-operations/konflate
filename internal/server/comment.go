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
	// .PR.HeadSHA, .PR.BaseRef, .PR.URL, ...
	PR api.PR
	// Diff is the rendered diff: .Diff.Impact.Resources, .Diff.Warnings,
	// .Diff.Images, .Diff.Failures, ... Comments render on a successful render, so
	// this is populated.
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
	t, err := template.New("comment").Option("missingkey=error").Parse(string(src))
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
	if s.commentTmpl == nil {
		return summaryMarkdown(env, reviewURL, admonitions)
	}
	data := commentTemplateData{
		PR:        env.PR,
		Diff:      env.Diff,
		ReviewURL: reviewURL,
		Summary:   summaryMarkdownBody(env, reviewURL, admonitions),
		Sections:  summarySectionsFor(env.Diff, admonitions),
	}
	var b strings.Builder
	if err := s.commentTmpl.Execute(&b, data); err != nil {
		s.log.Warn("custom PR comment template failed; using the default comment body",
			"pr", env.PR.Number, "error", err)
		return summaryMarkdown(env, reviewURL, admonitions)
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
