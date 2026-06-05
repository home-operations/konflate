package server

import (
	"cmp"
	"log/slog"
	"strings"
	"text/template"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// defaultMergeCommands is the basic per-forge merge CLI, rendered into the
// "copy to merge" affordance when KONFLATE_MERGE_COMMAND is unset. Operators
// override it to add a strategy (--squash/--rebase), branch cleanup, --auto,
// authentication flags, and so on. konflate never runs these — it only hands
// the reviewer a string to paste into their own shell.
var defaultMergeCommands = map[config.ForgeKind]string{
	config.ForgeGitHub:  "gh pr merge {{.Number}} --repo {{.Repo}}",
	config.ForgeGitLab:  "glab mr merge {{.Number}} --repo {{.Repo}}",
	config.ForgeForgejo: "tea pr merge {{.Number}} --repo {{.Repo}}",
}

// mergeCmdData is the merge-command template context. Deliberately minimal: only
// the PR number and the operator-configured repo path are exposed. Attacker-
// controlled fields (branch name, PR title) are NOT — the rendered string is
// pasted into a shell, so a `{{.Branch}}` of a PR named `$(rm -rf ~)` would be a
// command injection.
type mergeCmdData struct {
	Number int
	Repo   string
}

// newMergeTemplate parses the effective merge-command template — the operator
// override, or the forge default. A parse error disables the feature (logged,
// nil returned) rather than failing startup: it's a paste-in convenience, not
// load-bearing. nil is also returned for an unknown forge with no override.
func newMergeTemplate(cfg *config.Config, log *slog.Logger) *template.Template {
	src := cmp.Or(cfg.MergeCommand, defaultMergeCommands[cfg.Forge.Kind])
	if src == "" {
		return nil
	}
	t, err := template.New("merge").Option("missingkey=error").Parse(src)
	if err != nil {
		log.Warn("invalid KONFLATE_MERGE_COMMAND; merge command disabled", "error", err)
		return nil
	}
	return t
}

// mergeCommand renders the merge command for pr, or "" when the feature is off
// (no template) or the PR isn't open (merging a closed/merged PR is moot).
func (s *Server) mergeCommand(pr api.PR) string {
	if s.mergeTmpl == nil || !pr.Open {
		return ""
	}
	var b strings.Builder
	if err := s.mergeTmpl.Execute(&b, mergeCmdData{Number: pr.Number, Repo: s.cfg.Forge.RepoPath}); err != nil {
		return "" // misconfigured template referencing an unknown field; stay quiet
	}
	return b.String()
}
