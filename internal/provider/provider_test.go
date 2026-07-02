package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

const githubPullsJSON = `[
  {
    "number": 7,
    "title": "feat: add widget",
    "state": "open",
    "draft": false,
    "created_at": "2026-06-01T12:00:00Z",
    "user": {"login": "octocat", "avatar_url": "https://avatars.example/u/octocat.png"},
    "head": {"ref": "feat/widget", "sha": "deadbeefcafe"},
    "base": {"ref": "main"},
    "labels": [{"name": "enhancement", "color": "a2eeef"}, {"name": "area/ui", "color": "d4c5f9"}],
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

	prs, err := p.ListPRs(t.Context())
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
	if !slices.Equal(got.Labels, []api.Label{{Name: "enhancement", Color: "a2eeef"}, {Name: "area/ui", Color: "d4c5f9"}}) {
		t.Errorf("labels = %v", got.Labels)
	}
	if got.URL != "https://github.com/acme/web/pull/7" {
		t.Errorf("url = %q", got.URL)
	}
	if got.AuthorAvatar != "https://avatars.example/u/octocat.png" {
		t.Errorf("authorAvatar = %q", got.AuthorAvatar)
	}
	if got.CreatedAt.Format("2006-01-02") != "2026-06-01" {
		t.Errorf("createdAt = %v", got.CreatedAt)
	}
}

// TestGitHubProvider_ListPRsPaginates verifies ListPRs follows rel="next" across
// pages — without it, a repo with more open PRs than one page silently lost the
// rest. (The loop is structurally identical in the gitlab and forgejo providers.)
func TestGitHubProvider_ListPRsPaginates(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"number":2,"state":"open","head":{"ref":"b","sha":"s2"},"base":{"ref":"main"}}]`))
			return
		}
		// Page 1 advertises a next page, so the provider must request page 2.
		w.Header().Set("Link", `<`+r.URL.Path+`?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"number":1,"state":"open","head":{"ref":"a","sha":"s1"},"base":{"ref":"main"}}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	p := &githubProvider{client: client, owner: "acme", repo: "web"}

	prs, err := p.ListPRs(t.Context())
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs across pages, want 2 (pagination must follow rel=next)", len(prs))
	}
	if prs[0].Number != 1 || prs[1].Number != 2 {
		t.Errorf("paged PR numbers = %d, %d; want 1, 2", prs[0].Number, prs[1].Number)
	}
}

// TestGitHubProvider_GetPRNotFound verifies a 404 maps to ErrPRNotFound (so the
// server reaps a deleted PR rather than looping). The gitlab/forgejo providers
// use the same resp.StatusCode == 404 check.
func TestGitHubProvider_GetPRNotFound(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	p := &githubProvider{client: client, owner: "acme", repo: "web"}

	if _, err := p.GetPR(t.Context(), 9); !errors.Is(err, ErrPRNotFound) {
		t.Fatalf("GetPR on a 404 = %v, want ErrPRNotFound", err)
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

// TestNewGitHubReadAuth covers the read client's credential selection: a
// configured GitHub App authenticates reads via its installation token — the same
// identity write-back uses, lifting the 60/hr anonymous API rate limit — a read
// PAT is the fallback, and with neither the client is anonymous.
func TestNewGitHubReadAuth(t *testing.T) {
	t.Parallel()
	gh := config.ForgeURI{Kind: config.ForgeGitHub, RepoPath: "acme/web"}

	app, err := newGitHub(&config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: testRSAKeyPEM(t)})
	if err != nil {
		t.Fatalf("newGitHub (app): %v", err)
	}
	if _, ok := app.client.Client().Transport.(*installTransport); !ok {
		t.Errorf("App-configured read client transport = %T, want *installTransport", app.client.Client().Transport)
	}

	anon, err := newGitHub(&config.Config{Forge: gh})
	if err != nil {
		t.Fatalf("newGitHub (anon): %v", err)
	}
	if _, ok := anon.client.Client().Transport.(*installTransport); ok {
		t.Error("read client without App creds must not use App installation auth")
	}
}

// TestGitTokenSource covers the renderer's git credential selection, which must
// mirror the read client's: a configured GitHub App yields a (lazy) token source so
// the clone authenticates as the same installation reads use, a PAT is returned
// verbatim (the x-access-token password), and with neither the source is anonymous.
func TestGitTokenSource(t *testing.T) {
	t.Parallel()
	gh := config.ForgeURI{Kind: config.ForgeGitHub, RepoPath: "acme/web"}

	// PAT: returned verbatim.
	src, err := GitTokenSource(&config.Config{Forge: gh, Token: "pat-123"})
	if err != nil {
		t.Fatalf("GitTokenSource (pat): %v", err)
	}
	if tok, err := src(t.Context()); err != nil || tok != "pat-123" {
		t.Errorf("pat source = (%q, %v), want (\"pat-123\", nil)", tok, err)
	}

	// Neither credential: anonymous (empty token ⇒ the mirror clones without auth).
	src, err = GitTokenSource(&config.Config{Forge: gh})
	if err != nil {
		t.Fatalf("GitTokenSource (anon): %v", err)
	}
	if tok, err := src(t.Context()); err != nil || tok != "" {
		t.Errorf("anon source = (%q, %v), want (\"\", nil)", tok, err)
	}

	// GitHub App: a token source is built (no network until it's invoked).
	src, err = GitTokenSource(&config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: testRSAKeyPEM(t)})
	if err != nil {
		t.Fatalf("GitTokenSource (app): %v", err)
	}
	if src == nil {
		t.Error("App-configured git token source must not be nil")
	}
	// A malformed App key is rejected here — the App branch parses it, the static
	// PAT path never would, so this also proves App config takes the App branch.
	if _, err := GitTokenSource(&config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: "not-a-pem-key"}); err == nil {
		t.Error("GitTokenSource must reject a malformed App private key")
	}
}

// TestRateLimit covers the rate-limit classifier the server uses to turn a failed
// PR-list into a time-bounded UI status: GitHub's primary RateLimitError carries
// the reset time, the secondary AbuseRateLimitError a retry-after, and both are
// recognized through the wrapped error chain; an ordinary error is not a limit.
func TestRateLimit(t *testing.T) {
	t.Parallel()
	reset := time.Now().Add(11 * time.Minute).Truncate(time.Second)
	wrapped := fmt.Errorf("github: list PRs: %w", &github.RateLimitError{
		Rate:    github.Rate{Reset: github.Timestamp{Time: reset}},
		Message: "API rate limit exceeded",
	})
	if got, ok := RateLimit(wrapped); !ok || !got.Equal(reset) {
		t.Errorf("RateLimit(primary) = (%v, %v); want (%v, true)", got, ok, reset)
	}

	after := 30 * time.Second
	if _, ok := RateLimit(fmt.Errorf("x: %w", &github.AbuseRateLimitError{RetryAfter: &after})); !ok {
		t.Error("RateLimit(secondary/abuse) ok = false; want true")
	}

	if _, ok := RateLimit(errors.New("connection refused")); ok {
		t.Error("RateLimit(plain error) ok = true; want false")
	}
}
