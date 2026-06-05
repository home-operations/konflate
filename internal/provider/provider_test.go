package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/config"
)

const githubPullsJSON = `[
  {
    "number": 7,
    "title": "feat: add widget",
    "state": "open",
    "draft": false,
    "user": {"login": "octocat", "avatar_url": "https://avatars.example/u/octocat.png"},
    "head": {"ref": "feat/widget", "sha": "deadbeefcafe"},
    "base": {"ref": "main"},
    "labels": [{"name": "enhancement"}, {"name": "area/ui"}],
    "html_url": "https://github.com/acme/web/pull/7"
  }
]`

func TestGitHubProvider_ListPRs(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(githubPullsJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	p := &githubProvider{client: client, owner: "acme", repo: "web"}

	prs, err := p.ListPRs(context.Background())
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(prs))
	}
	got := prs[0]
	want := struct {
		number                         int
		title, author, head, sha, base string
	}{7, "feat: add widget", "octocat", "feat/widget", "deadbeefcafe", "main"}
	if got.Number != want.number || got.Title != want.title || got.Author != want.author ||
		got.HeadRef != want.head || got.HeadSHA != want.sha || got.BaseRef != want.base {
		t.Errorf("mapping wrong:\n got %+v", got)
	}
	if got.State != "open" || got.Draft {
		t.Errorf("state/draft = %q/%v", got.State, got.Draft)
	}
	if !slices.Equal(got.Labels, []string{"enhancement", "area/ui"}) {
		t.Errorf("labels = %v", got.Labels)
	}
	if got.URL != "https://github.com/acme/web/pull/7" {
		t.Errorf("url = %q", got.URL)
	}
	if got.AuthorAvatar != "https://avatars.example/u/octocat.png" {
		t.Errorf("authorAvatar = %q", got.AuthorAvatar)
	}
}

func TestNew_DispatchesByForge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind     config.ForgeKind
		wantType string
	}{
		{config.ForgeGitHub, "*provider.githubProvider"},
		{config.ForgeGitLab, "*provider.gitlabProvider"},
		{config.ForgeForgejo, "*provider.forgejoProvider"},
	}
	for _, tt := range cases {
		t.Run(string(tt.kind), func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Token: "test-token",
				Forge: config.ForgeURI{Kind: tt.kind, RepoPath: "acme/web"},
			}
			p, err := New(cfg)
			if err != nil {
				t.Fatalf("New(%s): %v", tt.kind, err)
			}
			if got := fmt.Sprintf("%T", p); got != tt.wantType {
				t.Errorf("New(%s) = %s, want %s", tt.kind, got, tt.wantType)
			}
		})
	}
}
