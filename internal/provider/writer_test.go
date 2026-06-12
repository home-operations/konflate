package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

func TestGitHubWriter_SetStatus(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var got struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/statuses/"+sha, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	wr := &githubWriter{client: client, owner: "acme", repo: "web"}

	err = wr.SetStatus(context.Background(), api.PR{Number: 7, HeadSHA: sha}, Status{
		State: StatusSuccess, Description: "rendered", TargetURL: "https://k.example/#/pr/7", Context: "konflate",
	})
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.State != "success" || got.Context != "konflate" ||
		got.TargetURL != "https://k.example/#/pr/7" || got.Description != "rendered" {
		t.Errorf("status payload = %+v", got)
	}
}

func TestNewWriter_NilWhenDisabled(t *testing.T) {
	t.Parallel()
	w, err := NewWriter(&config.Config{}) // no write credential
	if err != nil || w != nil {
		t.Fatalf("NewWriter with no credential = (%v, %v); want (nil, nil)", w, err)
	}
}

func TestGitlabBuildState(t *testing.T) {
	t.Parallel()
	cases := map[string]gitlab.BuildStateValue{
		StatusSuccess: gitlab.Success,
		StatusFailure: gitlab.Failed, // GitLab uses "failed", not "failure"
		StatusPending: gitlab.Pending,
		"anything":    gitlab.Pending, // unknown maps to pending
	}
	for in, want := range cases {
		if got := gitlabBuildState(in); got != want {
			t.Errorf("gitlabBuildState(%q) = %q, want %q", in, got, want)
		}
	}
}
