package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
)

// TestGitHubProvider_Checks merges legacy commit statuses with check runs into
// one rollup: a single failure anywhere makes the head red, an unfinished run
// keeps it pending.
func TestGitHubProvider_Checks(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/commits/"+sha+"/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"success","statuses":[{"state":"success"},{"state":"pending"}]}`))
	})
	mux.HandleFunc("/repos/acme/web/commits/"+sha+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":3,"check_runs":[` +
			`{"status":"completed","conclusion":"success"},` +
			`{"status":"completed","conclusion":"failure"},` +
			`{"status":"in_progress"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	p := &githubProvider{client: client, owner: "acme", repo: "web"}

	got, err := p.Checks(context.Background(), api.PR{Number: 7, HeadSHA: sha})
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	// statuses: 1 success + 1 pending; runs: 1 success + 1 failure + 1 in_progress.
	// → passed 2, failed 1, pending 2 (total 5), and a failure makes it red.
	if got.State != api.CheckFailure {
		t.Errorf("state = %q, want failure", got.State)
	}
	if got.Total != 5 || got.Passed != 2 || got.Failed != 1 {
		t.Errorf("rollup = %+v, want total 5 / passed 2 / failed 1", got)
	}
}

// TestGitHubProvider_ChecksNoSHA: a PR with no head SHA makes no API calls and
// reports nothing (the client is never touched).
func TestGitHubProvider_ChecksNoSHA(t *testing.T) {
	t.Parallel()
	p := &githubProvider{owner: "acme", repo: "web"}
	got, err := p.Checks(context.Background(), api.PR{Number: 1})
	if err != nil || got.State != api.CheckNone {
		t.Fatalf("got %+v, %v; want none / no error", got, err)
	}
}
