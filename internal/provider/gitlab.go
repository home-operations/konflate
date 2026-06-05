package provider

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

type gitlabProvider struct {
	client  *gitlab.Client
	project string // full project path, e.g. "group/subgroup/repo"
}

func newGitLab(cfg *config.Config) (*gitlabProvider, error) {
	var opts []gitlab.ClientOptionFunc
	if host := cfg.Forge.Host; host != "" {
		opts = append(opts, gitlab.WithBaseURL("https://"+host))
	}
	client, err := gitlab.NewClient(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: new client: %w", err)
	}
	return &gitlabProvider{client: client, project: cfg.Forge.RepoPath}, nil
}

func (p *gitlabProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	state := "opened"
	order := "updated_at"
	mrs, _, err := p.client.MergeRequests.ListProjectMergeRequests(p.project, &gitlab.ListProjectMergeRequestsOptions{
		State:       &state,
		OrderBy:     &order,
		ListOptions: gitlab.ListOptions{PerPage: 50},
	}, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("gitlab: list MRs: %w", err)
	}
	out := make([]api.PR, 0, len(mrs))
	for _, mr := range mrs {
		out = append(out, gitlabToPR(mr))
	}
	return out, nil
}

func (p *gitlabProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	mr, _, err := p.client.MergeRequests.GetMergeRequest(p.project, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		return api.PR{}, fmt.Errorf("gitlab: get MR !%d: %w", number, err)
	}
	return gitlabToPR(&mr.BasicMergeRequest), nil
}

func gitlabToPR(mr *gitlab.BasicMergeRequest) api.PR {
	author := ""
	if mr.Author != nil {
		author = mr.Author.Username
	}
	return api.PR{
		Number:  int(mr.IID), // GitLab's per-project MR number
		Title:   mr.Title,
		Author:  author,
		State:   mr.State,
		Open:    mr.State == "opened", // GitLab's open state is "opened"
		Merged:  mr.State == "merged", // and it exposes a distinct "merged" state
		Draft:   mr.Draft,
		HeadRef: mr.SourceBranch,
		HeadSHA: mr.SHA,
		BaseRef: mr.TargetBranch,
		Labels:  []string(mr.Labels),
		URL:     mr.WebURL,
	}
}
