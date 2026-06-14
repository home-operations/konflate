package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// RateLimit reports whether err is a forge rate-limit rejection and, if so, when
// the limit resets — so the server can surface a clear, time-bounded status to the
// UI rather than a generic failure. GitHub's SDK returns a typed
// *github.RateLimitError (carrying the reset time) for the primary limit and
// *github.AbuseRateLimitError (a retry-after) for secondary limits; both are
// matched through the wrapped error chain. Non-GitHub forges and non-rate-limit
// errors report false.
func RateLimit(err error) (resetAt time.Time, ok bool) {
	if rl, ok := errors.AsType[*github.RateLimitError](err); ok {
		return rl.Rate.Reset.Time, true
	}
	if ab, ok := errors.AsType[*github.AbuseRateLimitError](err); ok {
		if ab.RetryAfter != nil {
			return time.Now().Add(*ab.RetryAfter), true
		}
		return time.Time{}, true
	}
	return time.Time{}, false
}

type githubProvider struct {
	client      *github.Client
	owner, repo string
}

func newGitHub(cfg *config.Config) (*githubProvider, error) {
	client, err := newGitHubReadClient(cfg)
	if err != nil {
		return nil, err
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &githubProvider{client: client, owner: owner, repo: repo}, nil
}

// newGitHubReadClient builds the read API client by credential precedence,
// mirroring newGitHubWriteClient: a configured GitHub App is konflate's forge
// identity for reads too — its installation token lifts the 60/hr anonymous limit
// and reaches private repos, the same credential write-back uses. A read PAT
// (KONFLATE_TOKEN) is the fallback; with neither, reads are anonymous.
func newGitHubReadClient(cfg *config.Config) (*github.Client, error) {
	if cfg.AppConfigured() {
		client, _, err := newGitHubAppInstallClient(cfg)
		return client, err
	}
	var opts []github.ClientOptionsFunc
	if cfg.Token != "" {
		opts = append(opts, github.WithAuthToken(cfg.Token))
	}
	opts = append(opts, githubEnterpriseOpts(cfg.Forge.Host)...)
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: new client: %w", err)
	}
	return client, nil
}

// githubEnterpriseOpts returns the base/upload-URL option for a self-hosted
// GitHub Enterprise Server host, or nil for github.com (host == ""). Shared by
// the read provider and the Writer.
func githubEnterpriseOpts(host string) []github.ClientOptionsFunc {
	if host == "" {
		return nil
	}
	base := "https://" + host + "/"
	return []github.ClientOptionsFunc{github.WithEnterpriseURLs(base, base)}
}

func (p *githubProvider) ListPRs(ctx context.Context) ([]api.PR, error) {
	opts := &github.PullRequestListOptions{
		State:       stateOpen,
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var out []api.PR
	for {
		prs, resp, err := p.client.PullRequests.List(ctx, p.owner, p.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("github: list PRs: %w", err)
		}
		for _, pr := range prs {
			out = append(out, githubToPR(pr))
		}
		if resp.NextPage == 0 {
			break // last page: without this loop, repos with >100 open PRs lost the rest
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (p *githubProvider) GetPR(ctx context.Context, number int) (api.PR, error) {
	pr, resp, err := p.client.PullRequests.Get(ctx, p.owner, p.repo, number)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return api.PR{}, fmt.Errorf("github: get PR #%d: %w", number, ErrPRNotFound)
		}
		return api.PR{}, fmt.Errorf("github: get PR #%d: %w", number, err)
	}
	return githubToPR(pr), nil
}

// Checks rolls up the head commit's CI: legacy commit statuses (GetCombinedStatus)
// merged with check runs (GitHub Actions and other Checks-API apps report only as
// check runs), since GitHub's own PR view combines the two. One page of each (100)
// covers any realistic PR; nothing here paginates further.
func (p *githubProvider) Checks(ctx context.Context, pr api.PR) (api.CheckRollup, error) {
	if pr.HeadSHA == "" {
		return api.CheckRollup{}, nil
	}
	var passed, failed, pending int

	cs, _, err := p.client.Repositories.GetCombinedStatus(ctx, p.owner, p.repo, pr.HeadSHA, &github.ListOptions{PerPage: 100})
	if err != nil {
		return api.CheckRollup{}, fmt.Errorf("github: combined status #%d: %w", pr.Number, err)
	}
	for _, s := range cs.Statuses {
		switch s.GetState() {
		case stateSuccess:
			passed++
		case "pending":
			pending++
		default: // failure, error
			failed++
		}
	}

	runs, _, err := p.client.Checks.ListCheckRunsForRef(ctx, p.owner, p.repo, pr.HeadSHA,
		&github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		return api.CheckRollup{}, fmt.Errorf("github: check runs #%d: %w", pr.Number, err)
	}
	for _, r := range runs.CheckRuns {
		switch {
		case r.GetStatus() != "completed":
			pending++
		case isPassingConclusion(r.GetConclusion()):
			passed++
		default:
			failed++
		}
	}
	return api.Rollup(passed, failed, pending), nil
}

// isPassingConclusion reports whether a completed check run's conclusion counts
// as green. neutral and skipped are non-blocking; failure, timed_out, cancelled,
// action_required, startup_failure and stale all count as a failure.
func isPassingConclusion(conclusion string) bool {
	switch conclusion {
	case stateSuccess, "neutral", "skipped":
		return true
	default:
		return false
	}
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
		// Cross-repo when the head repo differs from the base repo. A nil/deleted
		// head repo yields "" != base, i.e. treated as a fork (fail safe).
		Fork:   pr.GetHead().GetRepo().GetFullName() != pr.GetBase().GetRepo().GetFullName(),
		Labels: labels,
		URL:    pr.GetHTMLURL(),
	}
}
