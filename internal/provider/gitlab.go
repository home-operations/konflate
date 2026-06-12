package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

type gitlabProvider struct {
	client  *gitlab.Client
	project string // full project path, e.g. "group/subgroup/repo"
}

func newGitLab(cfg *config.Config) (*gitlabProvider, error) {
	client, err := gitlab.NewClient(cfg.Token, gitlabHostOpts(cfg.Forge.Host)...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: new client: %w", err)
	}
	return &gitlabProvider{client: client, project: cfg.Forge.RepoPath}, nil
}

// gitlabHostOpts returns the base-URL option for a self-hosted GitLab host, or
// nil for gitlab.com (host == ""). Shared by the read provider and the Writer.
func gitlabHostOpts(host string) []gitlab.ClientOptionFunc {
	if host == "" {
		return nil
	}
	return []gitlab.ClientOptionFunc{gitlab.WithBaseURL("https://" + host)}
}

func (p *gitlabProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	state := "opened"
	order := "updated_at"
	opts := &gitlab.ListProjectMergeRequestsOptions{
		State:       &state,
		OrderBy:     &order,
		ListOptions: gitlab.ListOptions{PerPage: 100},
	}
	var out []api.PR
	for {
		mrs, resp, err := p.client.MergeRequests.ListProjectMergeRequests(p.project, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("gitlab: list MRs: %w", err)
		}
		for _, mr := range mrs {
			out = append(out, gitlabToPR(mr))
		}
		if resp.NextPage == 0 {
			break // last page: without this loop, projects with >100 open MRs lost the rest
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (p *gitlabProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	mr, resp, err := p.client.MergeRequests.GetMergeRequest(p.project, int64(number), nil, gitlab.WithContext(ctx))
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return api.PR{}, fmt.Errorf("gitlab: get MR !%d: %w", number, ErrPRNotFound)
		}
		return api.PR{}, fmt.Errorf("gitlab: get MR !%d: %w", number, err)
	}
	return gitlabToPR(&mr.BasicMergeRequest), nil
}

// Checks rolls up the head commit's CI statuses — GitLab pipeline jobs surface
// as commit statuses. An allow_failure job that failed doesn't count against the
// rollup; canceled and manual jobs are neither pass nor fail (they don't count).
func (p *gitlabProvider) Checks(ctx context.Context, pr api.PR) (api.CheckRollup, error) {
	if pr.HeadSHA == "" {
		return api.CheckRollup{}, nil
	}
	statuses, _, err := p.client.Commits.GetCommitStatuses(p.project, pr.HeadSHA,
		&gitlab.GetCommitStatusesOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}, gitlab.WithContext(ctx))
	if err != nil {
		return api.CheckRollup{}, fmt.Errorf("gitlab: commit statuses !%d: %w", pr.Number, err)
	}
	var passed, failed, pending int
	for _, s := range statuses {
		switch s.Status {
		case stateSuccess, "skipped":
			passed++
		case "failed":
			if s.AllowFailure {
				passed++
			} else {
				failed++
			}
		case "canceled", "manual":
			// neither pass nor fail — leave out of the rollup
		default: // created, waiting_for_resource, preparing, pending, running, scheduled
			pending++
		}
	}
	return api.Rollup(passed, failed, pending), nil
}

func gitlabToPR(mr *gitlab.BasicMergeRequest) api.PR {
	author, avatar := "", ""
	if mr.Author != nil {
		author = mr.Author.Username
		avatar = mr.Author.AvatarURL
	}
	// GitLab's MR list carries label names only (no color).
	labels := make([]api.Label, 0, len(mr.Labels))
	for _, name := range mr.Labels {
		labels = append(labels, api.Label{Name: name})
	}
	var created time.Time
	if mr.CreatedAt != nil {
		created = *mr.CreatedAt
	}
	return api.PR{
		Number:       int(mr.IID), // GitLab's per-project MR number
		Title:        mr.Title,
		Author:       author,
		AuthorAvatar: avatar,
		CreatedAt:    created,
		State:        mr.State,
		Open:         mr.State == "opened", // GitLab's open state is "opened"
		Merged:       mr.State == "merged", // and it exposes a distinct "merged" state
		Draft:        mr.Draft,
		HeadRef:      mr.SourceBranch,
		HeadSHA:      mr.SHA,
		BaseRef:      mr.TargetBranch,
		// Cross-project (fork) when the MR source and target projects differ.
		Fork:   mr.SourceProjectID != mr.TargetProjectID,
		Labels: labels,
		URL:    mr.WebURL,
	}
}
