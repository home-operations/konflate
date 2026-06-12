package provider

import (
	"context"
	"fmt"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/google/go-github/v88/github"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// Write-back is opt-in and separate from the read-only Provider: it exists only
// when an operator configures a write credential, and konflate's HTTP surface
// never triggers it (writes come from konflate's own render loop). The states
// konflate reports, mapped per forge in each SetStatus.
const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusPending = "pending"
)

// Status is one commit-status update konflate writes onto a PR's head.
type Status struct {
	State       string // StatusSuccess | StatusFailure | StatusPending
	Description string // short summary shown on the PR
	TargetURL   string // where the status links — konflate's review URL
	Context     string // the status name on the PR (e.g. "konflate")
}

// Writer reports konflate's own results back to the forge — commit statuses now,
// PR comments later. It is deliberately separate from the read-only Provider and
// is built only when a write credential is configured (NewWriter returns a nil
// Writer otherwise), so the read path can never accidentally write.
type Writer interface {
	SetStatus(ctx context.Context, pr api.PR, st Status) error
}

// NewWriter builds the forge Writer from the configured write credential, or a
// nil Writer when write-back is disabled (no credential — the read-only default).
func NewWriter(cfg *config.Config) (Writer, error) {
	if !cfg.WriteEnabled() {
		return nil, nil
	}
	switch cfg.Forge.Kind {
	case config.ForgeGitHub:
		return newGitHubWriter(cfg)
	case config.ForgeGitLab:
		return newGitLabWriter(cfg)
	case config.ForgeForgejo:
		return newForgejoWriter(cfg)
	default:
		return nil, fmt.Errorf("provider: write-back unsupported for forge %q", cfg.Forge.Kind)
	}
}

// strOrNil maps "" to a nil *string so an empty optional field is omitted rather
// than sent as a blank value.
func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- GitHub ---

type githubWriter struct {
	client      *github.Client
	owner, repo string
}

func newGitHubWriter(cfg *config.Config) (*githubWriter, error) {
	if cfg.WriteToken == "" {
		// GitHub App auth (mint an installation token from the client id + key) is
		// wired in a follow-up; until then a write PAT is required.
		return nil, fmt.Errorf("github: write-back needs KONFLATE_WRITE_TOKEN (App auth not yet wired)")
	}
	opts := []github.ClientOptionsFunc{github.WithAuthToken(cfg.WriteToken)}
	if host := cfg.Forge.Host; host != "" {
		base := "https://" + host + "/"
		opts = append(opts, github.WithEnterpriseURLs(base, base))
	}
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: new write client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &githubWriter{client: client, owner: owner, repo: repo}, nil
}

func (w *githubWriter) SetStatus(ctx context.Context, pr api.PR, st Status) error {
	state := st.State // GitHub's states match konflate's: success / failure / pending
	_, _, err := w.client.Repositories.CreateStatus(ctx, w.owner, w.repo, pr.HeadSHA, github.RepoStatus{
		State:       &state,
		TargetURL:   strOrNil(st.TargetURL),
		Description: strOrNil(st.Description),
		Context:     strOrNil(st.Context),
	})
	if err != nil {
		return fmt.Errorf("github: set status #%d: %w", pr.Number, err)
	}
	return nil
}

// --- Forgejo ---

type forgejoWriter struct {
	client      *forgejo.Client
	owner, repo string
}

func newForgejoWriter(cfg *config.Config) (*forgejoWriter, error) {
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("forgejo: write-back needs KONFLATE_WRITE_TOKEN")
	}
	base := "https://codeberg.org"
	if host := cfg.Forge.Host; host != "" {
		base = "https://" + host
	}
	client, err := forgejo.NewClient(base,
		forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken(cfg.WriteToken))
	if err != nil {
		return nil, fmt.Errorf("forgejo: new write client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &forgejoWriter{client: client, owner: owner, repo: repo}, nil
}

func (w *forgejoWriter) SetStatus(_ context.Context, pr api.PR, st Status) error {
	// The Forgejo SDK can't take a context (see provider ListPRs).
	_, _, err := w.client.CreateStatus(w.owner, w.repo, pr.HeadSHA, forgejo.CreateStatusOption{
		State:       forgejo.StatusState(st.State), // success / failure / pending are valid states
		TargetURL:   st.TargetURL,
		Description: st.Description,
		Context:     st.Context,
	})
	if err != nil {
		return fmt.Errorf("forgejo: set status #%d: %w", pr.Number, err)
	}
	return nil
}

// --- GitLab ---

type gitlabWriter struct {
	client  *gitlab.Client
	project string
}

func newGitLabWriter(cfg *config.Config) (*gitlabWriter, error) {
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("gitlab: write-back needs KONFLATE_WRITE_TOKEN")
	}
	var opts []gitlab.ClientOptionFunc
	if host := cfg.Forge.Host; host != "" {
		opts = append(opts, gitlab.WithBaseURL("https://"+host))
	}
	client, err := gitlab.NewClient(cfg.WriteToken, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: new write client: %w", err)
	}
	return &gitlabWriter{client: client, project: cfg.Forge.RepoPath}, nil
}

func (w *gitlabWriter) SetStatus(ctx context.Context, pr api.PR, st Status) error {
	_, _, err := w.client.Commits.SetCommitStatus(w.project, pr.HeadSHA, &gitlab.SetCommitStatusOptions{
		State:       gitlabBuildState(st.State),
		Name:        strOrNil(st.Context), // GitLab shows the status under its name
		Description: strOrNil(st.Description),
		TargetURL:   strOrNil(st.TargetURL),
	}, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("gitlab: set status !%d: %w", pr.Number, err)
	}
	return nil
}

// gitlabBuildState maps konflate's state to GitLab's pipeline build state
// (GitLab uses "failed", not "failure").
func gitlabBuildState(s string) gitlab.BuildStateValue {
	switch s {
	case StatusSuccess:
		return gitlab.Success
	case StatusFailure:
		return gitlab.Failed
	default:
		return gitlab.Pending
	}
}
