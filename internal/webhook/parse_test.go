package webhook

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/home-operations/konflate/internal/config"
)

func hdr(k, v string) http.Header {
	h := make(http.Header)
	h.Set(k, v) // canonicalizes the key, exactly as net/http does for real requests
	return h
}

func TestParse_StatusEvents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		forge   config.ForgeKind
		header  http.Header
		body    string
		wantSHA string
	}{
		{"github status", config.ForgeGitHub, hdr("X-GitHub-Event", "status"),
			`{"sha":"abc123","state":"success"}`, "abc123"},
		{"github check_run", config.ForgeGitHub, hdr("X-GitHub-Event", "check_run"),
			`{"check_run":{"head_sha":"def456"}}`, "def456"},
		{"github check_suite", config.ForgeGitHub, hdr("X-GitHub-Event", "check_suite"),
			`{"check_suite":{"head_sha":"aaa111"}}`, "aaa111"},
		{"forgejo status via commit.sha", config.ForgeForgejo, hdr("X-Gitea-Event", "status"),
			`{"commit":{"sha":"bbb222"}}`, "bbb222"},
		{"gitlab pipeline", config.ForgeGitLab, nil,
			`{"object_kind":"pipeline","object_attributes":{"sha":"ccc333"}}`, "ccc333"},
		{"gitlab job", config.ForgeGitLab, nil,
			`{"object_kind":"build","sha":"ddd444"}`, "ddd444"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := Parse(tt.forge, tt.header, []byte(tt.body))
			if ev.HeadSHA != tt.wantSHA {
				t.Errorf("HeadSHA = %q, want %q", ev.HeadSHA, tt.wantSHA)
			}
			if ev.Relist || ev.PR != 0 {
				t.Errorf("a status event must not relist or carry a PR, got %+v", ev)
			}
		})
	}
	// A status event with no SHA degrades to a relist (the safe fallback).
	if ev := Parse(config.ForgeGitHub, hdr("X-GitHub-Event", "status"), []byte(`{}`)); !ev.Relist || ev.HeadSHA != "" {
		t.Errorf("empty status payload should relist, got %+v", ev)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		forge      config.ForgeKind
		header     http.Header
		body       string
		wantPR     int
		wantRelist bool
	}{
		// GitHub
		{"github synchronize → that PR", config.ForgeGitHub, hdr("X-GitHub-Event", "pull_request"),
			`{"action":"synchronize","number":7,"pull_request":{"number":7}}`, 7, false},
		{"github opened → relist", config.ForgeGitHub, hdr("X-GitHub-Event", "pull_request"),
			`{"action":"opened","number":7}`, 7, true},
		{"github number falls back to pull_request.number", config.ForgeGitHub, hdr("X-GitHub-Event", "pull_request"),
			`{"action":"synchronize","pull_request":{"number":9}}`, 9, false},
		{"github non-PR event → relist", config.ForgeGitHub, hdr("X-GitHub-Event", "ping"), `{}`, 0, true},
		{"github malformed → relist", config.ForgeGitHub, hdr("X-GitHub-Event", "pull_request"), `not json`, 0, true},
		// GitHub's default content type wraps the JSON in a `payload=` form field.
		{"github form-encoded synchronize → that PR", config.ForgeGitHub, hdr("X-GitHub-Event", "pull_request"),
			"payload=" + url.QueryEscape(`{"action":"synchronize","number":7,"pull_request":{"number":7}}`), 7, false},

		// Forgejo / Gitea
		{"forgejo synchronized → that PR", config.ForgeForgejo, hdr("X-Gitea-Event", "pull_request"),
			`{"action":"synchronized","number":3}`, 3, false},
		{"forgejo closed → relist", config.ForgeForgejo, hdr("X-Gitea-Event", "pull_request"),
			`{"action":"closed","number":3}`, 3, true},

		// GitLab
		{"gitlab update → that MR", config.ForgeGitLab, nil,
			`{"object_kind":"merge_request","object_attributes":{"iid":5,"action":"update"}}`, 5, false},
		{"gitlab open → relist", config.ForgeGitLab, nil,
			`{"object_kind":"merge_request","object_attributes":{"iid":5,"action":"open"}}`, 5, true},
		{"gitlab non-MR payload → relist", config.ForgeGitLab, nil, `{"object_kind":"push"}`, 0, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ev := Parse(tt.forge, tt.header, []byte(tt.body))
			if ev.PR != tt.wantPR || ev.Relist != tt.wantRelist {
				t.Errorf("Parse() = {PR:%d Relist:%v}, want {PR:%d Relist:%v}", ev.PR, ev.Relist, tt.wantPR, tt.wantRelist)
			}
		})
	}
}
