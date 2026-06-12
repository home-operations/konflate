package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

type forgejoProvider struct {
	client      *forgejo.Client
	owner, repo string
}

func newForgejo(cfg *config.Config) (*forgejoProvider, error) {
	// Pin the API version so NewClient does not probe the server at
	// construction (which would make startup depend on network reachability and
	// eager token validation). Version is discovered lazily on the first real
	// call instead.
	opts := []forgejo.ClientOption{forgejo.SetForgejoVersion(forgejo.Version())}
	if cfg.Token != "" {
		opts = append(opts, forgejo.SetToken(cfg.Token))
	}
	client, err := forgejo.NewClient(forgejoBaseURL(cfg.Forge.Host), opts...)
	if err != nil {
		return nil, fmt.Errorf("forgejo: new client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &forgejoProvider{client: client, owner: owner, repo: repo}, nil
}

// forgejoBaseURL is the API base for a Forgejo host, defaulting to the
// codeberg.org cloud when host is empty. Shared by the read provider and the
// Writer.
func forgejoBaseURL(host string) string {
	if host == "" {
		return "https://codeberg.org"
	}
	return "https://" + host
}

func (p *forgejoProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	// The Forgejo SDK's list/get calls take no context.Context, so cancellation
	// can't be threaded through; ctx is accepted only to satisfy the Provider
	// interface and is intentionally unused.
	_ = ctx
	opts := forgejo.ListPullRequestsOptions{
		State:       forgejo.StateOpen,
		Sort:        "recentupdate",
		ListOptions: forgejo.ListOptions{PageSize: 50}, // Gitea/Forgejo caps the page size server-side
	}
	var out []api.PR
	for {
		prs, resp, err := p.client.ListRepoPullRequests(p.owner, p.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("forgejo: list PRs: %w", err)
		}
		for _, pr := range prs {
			out = append(out, forgejoToPR(pr))
		}
		if resp.NextPage == 0 {
			break // last page: without this loop, repos with >50 open PRs lost the rest
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (p *forgejoProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	_ = ctx // see ListPRs: the Forgejo SDK can't take a context.
	pr, resp, err := p.client.GetPullRequest(p.owner, p.repo, int64(number))
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return api.PR{}, fmt.Errorf("forgejo: get PR #%d: %w", number, ErrPRNotFound)
		}
		return api.PR{}, fmt.Errorf("forgejo: get PR #%d: %w", number, err)
	}
	return forgejoToPR(pr), nil
}

func forgejoToPR(pr *forgejo.PullRequest) api.PR {
	var author, avatar string
	if pr.Poster != nil {
		author = pr.Poster.UserName
		avatar = pr.Poster.AvatarURL
	}
	labels := make([]api.Label, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, api.Label{Name: l.Name, Color: l.Color})
	}
	var created time.Time
	if pr.Created != nil {
		created = *pr.Created
	}
	out := api.PR{
		Number:       int(pr.Index),
		Title:        pr.Title,
		Author:       author,
		AuthorAvatar: avatar,
		CreatedAt:    created,
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
	// Cross-repo (fork) when the head and base point at different repositories.
	if pr.Head != nil && pr.Base != nil {
		out.Fork = pr.Head.RepoID != pr.Base.RepoID
	}
	return out
}
