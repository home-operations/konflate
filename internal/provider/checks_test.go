package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/google/go-github/v89/github"

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

	got, err := p.Checks(t.Context(), api.PR{Number: 7, HeadSHA: sha})
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
	got, err := p.Checks(t.Context(), api.PR{Number: 1})
	if err != nil || got.State != api.CheckNone {
		t.Fatalf("got %+v, %v; want none / no error", got, err)
	}
}

// TestForgejoProvider_ChecksPagesAndDedups verifies the rollup walks ALL pages of
// commit statuses (GetCombinedStatus couldn't page, silently dropping contexts past
// the server's default page size) and reduces to the latest status per context —
// highest id wins — matching the combined endpoint's server-side dedup.
func TestForgejoProvider_ChecksPagesAndDedups(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var combinedHit bool
	mux := http.NewServeMux()
	// The old, unpaged combined-status endpoint must no longer be used. (The Forgejo
	// SDK prefixes every path with /api/v1.)
	mux.HandleFunc("/api/v1/repos/acme/web/commits/"+sha+"/status", func(w http.ResponseWriter, _ *http.Request) {
		combinedHit = true
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/repos/acme/web/commits/"+sha+"/statuses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[` +
				`{"id":4,"status":"pending","context":"ci/lint"},` +
				`{"id":2,"status":"warning","context":"ci/warn"}]`))
			return
		}
		// Page 1 advertises a next page via the Link header (what the SDK reads). It
		// also lists ci/build twice: the newer (id 5, success) must win over the
		// stale (id 1, pending), or the pending count would be wrong.
		w.Header().Set("Link", `<?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[` +
			`{"id":1,"status":"pending","context":"ci/build"},` +
			`{"id":5,"status":"success","context":"ci/build"},` +
			`{"id":3,"status":"failure","context":"ci/test"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := forgejo.NewClient(srv.URL,
		forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken("tok"))
	if err != nil {
		t.Fatalf("forgejo.NewClient: %v", err)
	}
	p := &forgejoProvider{client: client, owner: "acme", repo: "web"}

	got, err := p.Checks(t.Context(), api.PR{Number: 7, HeadSHA: sha})
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if combinedHit {
		t.Error("Checks still hit the unpaged combined-status endpoint")
	}
	// ci/build→success + ci/warn→warning (both passed), ci/test→failure, ci/lint→pending.
	// Total 4 (not 5) proves the stale ci/build pending was deduped away; passed 2 and a
	// present failure prove the second page wasn't dropped.
	if got.Total != 4 || got.Passed != 2 || got.Failed != 1 {
		t.Errorf("rollup = %+v, want total 4 / passed 2 / failed 1", got)
	}
	if got.State != api.CheckFailure {
		t.Errorf("state = %q, want failure (a failed context is present)", got.State)
	}
}
