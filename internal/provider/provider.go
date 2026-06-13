// Package provider is the read-only forge API surface: list and fetch pull
// requests across GitHub, GitLab, and Forgejo. Only one provider is
// instantiated per process, selected from the configured forge URI. konflate
// never writes to the forge.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// stateOpen is the forge state string for an open PR on GitHub and Forgejo
// (GitLab uses "opened"). The normalized api.PR.Open flag is derived from it.
const stateOpen = "open"

// stateSuccess is the "success" status string all three forges use in their
// commit-status and check-run payloads (see each provider's Checks).
const stateSuccess = "success"

// ErrPRNotFound is returned by GetPR when the forge reports the pull/merge
// request does not exist (HTTP 404) — it was deleted, not merged or closed. The
// server reaps such a PR instead of retrying the lookup every refresh.
var ErrPRNotFound = errors.New("provider: pull request not found")

// Provider lists and fetches pull requests for the configured repository.
type Provider interface {
	// ListPRs returns open pull requests, newest first.
	ListPRs(ctx context.Context) ([]api.PR, error)
	// GetPR returns a single PR (head ref + SHA, base branch, metadata) by number.
	GetPR(ctx context.Context, number int) (api.PR, error)
	// Checks returns the rolled-up CI status of the PR's head commit — the
	// combined commit statuses and/or check runs the forge shows as red/amber/
	// green. A head with no checks yields a CheckNone rollup (no error).
	Checks(ctx context.Context, pr api.PR) (api.CheckRollup, error)
}

// New builds the provider for the configured forge.
func New(cfg *config.Config) (Provider, error) {
	switch cfg.Forge.Kind {
	case config.ForgeGitHub:
		return newGitHub(cfg)
	case config.ForgeGitLab:
		return newGitLab(cfg)
	case config.ForgeForgejo:
		return newForgejo(cfg)
	default:
		return nil, fmt.Errorf("provider: unsupported forge %q", cfg.Forge.Kind)
	}
}

// GitTokenSource returns the credential the renderer uses to authenticate git
// clone/fetch over HTTPS, so a render reaches the forge with the same identity as
// the API client: a private repo can be cloned, and an authenticated clone isn't
// throttled by the anonymous rate limit. GitHub with App credentials yields a
// fresh installation token per call (they expire hourly); otherwise it returns the
// static PAT (KONFLATE_TOKEN), or "" when neither is configured — anonymous, for
// public repositories only. Only GitHub has an App path; GitLab and Forgejo always
// use the token. The returned function is safe for concurrent use.
func GitTokenSource(cfg *config.Config) (func(ctx context.Context) (string, error), error) {
	if cfg.Forge.Kind == config.ForgeGitHub && cfg.AppConfigured() {
		it, err := newInstallTransport(cfg)
		if err != nil {
			return nil, err
		}
		return it.token, nil
	}
	token := cfg.Token
	return func(context.Context) (string, error) { return token, nil }, nil
}

// ownerRepo splits an "owner/repo" path into its two parts.
func ownerRepo(repoPath string) (owner, repo string) {
	owner, repo, _ = strings.Cut(repoPath, "/")
	return owner, repo
}
