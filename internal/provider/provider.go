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

// ownerRepo splits an "owner/repo" path into its two parts.
func ownerRepo(repoPath string) (owner, repo string) {
	owner, repo, _ = strings.Cut(repoPath, "/")
	return owner, repo
}
