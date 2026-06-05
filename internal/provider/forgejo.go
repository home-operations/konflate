package provider

import (
	"context"
	"fmt"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

type forgejoProvider struct {
	client      *forgejo.Client
	owner, repo string
}

func newForgejo(cfg *config.Config) (*forgejoProvider, error) {
	base := "https://codeberg.org"
	if host := cfg.Forge.Host; host != "" {
		base = "https://" + host
	}
	// Pin the API version so NewClient does not probe the server at
	// construction (which would make startup depend on network reachability and
	// eager token validation). Version is discovered lazily on the first real
	// call instead.
	opts := []forgejo.ClientOption{forgejo.SetForgejoVersion(forgejo.Version())}
	if cfg.Token != "" {
		opts = append(opts, forgejo.SetToken(cfg.Token))
	}
	client, err := forgejo.NewClient(base, opts...)
	if err != nil {
		return nil, fmt.Errorf("forgejo: new client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &forgejoProvider{client: client, owner: owner, repo: repo}, nil
}

func (p *forgejoProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	prs, _, err := p.client.ListRepoPullRequests(p.owner, p.repo, forgejo.ListPullRequestsOptions{
		State:       forgejo.StateOpen,
		Sort:        "recentupdate",
		ListOptions: forgejo.ListOptions{PageSize: 50},
	})
	if err != nil {
		return nil, fmt.Errorf("forgejo: list PRs: %w", err)
	}
	_ = ctx
	out := make([]api.PR, 0, len(prs))
	for _, pr := range prs {
		out = append(out, forgejoToPR(pr))
	}
	return out, nil
}

func (p *forgejoProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	pr, _, err := p.client.GetPullRequest(p.owner, p.repo, int64(number))
	if err != nil {
		return api.PR{}, fmt.Errorf("forgejo: get PR #%d: %w", number, err)
	}
	_ = ctx
	return forgejoToPR(pr), nil
}

func forgejoToPR(pr *forgejo.PullRequest) api.PR {
	var author, avatar string
	if pr.Poster != nil {
		author = pr.Poster.UserName
		avatar = pr.Poster.AvatarURL
	}
	labels := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, l.Name)
	}
	out := api.PR{
		Number:       int(pr.Index),
		Title:        pr.Title,
		Author:       author,
		AuthorAvatar: avatar,
		State:        string(pr.State),
		Open:         string(pr.State) == stateOpen,
		Merged:       pr.HasMerged,
		Labels:       labels,
		URL:          pr.HTMLURL,
	}
	if pr.Head != nil {
		out.HeadRef, out.HeadSHA = pr.Head.Ref, pr.Head.Sha
	}
	if pr.Base != nil {
		out.BaseRef = pr.Base.Ref
	}
	return out
}
