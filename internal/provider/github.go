package provider

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

type githubProvider struct {
	client      *github.Client
	owner, repo string
}

func newGitHub(cfg *config.Config) (*githubProvider, error) {
	var opts []github.ClientOptionsFunc
	if cfg.Token != "" {
		opts = append(opts, github.WithAuthToken(cfg.Token))
	}
	if host := cfg.Forge.Host; host != "" { // GitHub Enterprise Server
		base := "https://" + host + "/"
		opts = append(opts, github.WithEnterpriseURLs(base, base))
	}
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: new client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &githubProvider{client: client, owner: owner, repo: repo}, nil
}

func (p *githubProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	opts := &github.PullRequestListOptions{
		State:       stateOpen,
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: 50},
	}
	prs, _, err := p.client.PullRequests.List(ctx, p.owner, p.repo, opts)
	if err != nil {
		return nil, fmt.Errorf("github: list PRs: %w", err)
	}
	out := make([]api.PR, 0, len(prs))
	for _, pr := range prs {
		out = append(out, githubToPR(pr))
	}
	return out, nil
}

func (p *githubProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	pr, _, err := p.client.PullRequests.Get(ctx, p.owner, p.repo, number)
	if err != nil {
		return api.PR{}, fmt.Errorf("github: get PR #%d: %w", number, err)
	}
	return githubToPR(pr), nil
}

func githubToPR(pr *github.PullRequest) api.PR {
	labels := make([]api.Label, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, api.Label{Name: l.GetName(), Color: l.GetColor()})
	}
	return api.PR{
		Number:       pr.GetNumber(),
		Title:        pr.GetTitle(),
		Author:       pr.GetUser().GetLogin(),
		AuthorAvatar: pr.GetUser().GetAvatarURL(),
		CreatedAt:    pr.GetCreatedAt().Time,
		State:        pr.GetState(),
		Open:         pr.GetState() == stateOpen,
		Merged:       pr.GetMerged(),
		Draft:        pr.GetDraft(),
		HeadRef:      pr.GetHead().GetRef(),
		HeadSHA:      pr.GetHead().GetSHA(),
		BaseRef:      pr.GetBase().GetRef(),
		Labels:       labels,
		URL:          pr.GetHTMLURL(),
	}
}
