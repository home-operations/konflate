package webhook

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/home-operations/konflate/internal/config"
)

// Event is the minimal information konflate extracts from a verified webhook.
type Event struct {
	// PR is the affected pull/merge request number, or 0 if the payload did not
	// identify one.
	PR int
	// Relist is true when the set of open PRs may have changed (opened, closed,
	// reopened, merged) — the caller should re-list rather than re-render one.
	Relist bool
	// HeadSHA is set for a CI-status event (commit status / check run / check
	// suite / pipeline / job): the commit the status is for. The caller maps it
	// to the open PR with that head and refreshes only its check rollup — no
	// re-list, no re-render. Empty for PR and relist events.
	HeadSHA string
}

// Parse extracts the affected PR from a verified webhook body. Anything it
// cannot confidently interpret yields Relist (a full re-list), which is always
// safe — at worst it does more work than necessary.
func Parse(forge config.ForgeKind, header http.Header, body []byte) Event {
	body = unwrapFormPayload(header, body)
	switch forge {
	case config.ForgeGitHub:
		switch header.Get("X-GitHub-Event") {
		case "pull_request":
			return parsePullRequest(true, body)
		case "status", "check_run", "check_suite":
			return parseStatus(body)
		default:
			return Event{Relist: true}
		}
	case config.ForgeForgejo:
		switch header.Get("X-Gitea-Event") {
		case "pull_request":
			return parsePullRequest(true, body)
		case "status":
			return parseStatus(body)
		default:
			return Event{Relist: true}
		}
	case config.ForgeGitLab:
		return parseGitLab(body)
	}
	return Event{Relist: true}
}

// unwrapFormPayload returns the JSON document from a webhook body. GitHub and
// Gitea/Forgejo default to the application/x-www-form-urlencoded content type,
// which wraps the JSON in a `payload=` form field; unwrap it so the parsers
// always see raw JSON — otherwise a content event fails to parse and degrades to
// a full re-list. Signature verification runs over the original body upstream,
// so unwrapping here never affects authentication.
func unwrapFormPayload(header http.Header, body []byte) []byte {
	if !strings.HasPrefix(header.Get("Content-Type"), "application/x-www-form-urlencoded") &&
		!bytes.HasPrefix(body, []byte("payload=")) {
		return body
	}
	if v, err := url.ParseQuery(string(body)); err == nil {
		if p := v.Get("payload"); p != "" {
			return []byte(p)
		}
	}
	return body
}

// parsePullRequest handles GitHub and Forgejo/Gitea, whose "pull_request"
// payloads share a shape: {action, number, pull_request:{number}}. The actions
// that change the open-PR set are the same on both.
func parsePullRequest(isPullRequest bool, body []byte) Event {
	if !isPullRequest {
		return Event{Relist: true}
	}
	var p struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{Relist: true}
	}
	return Event{
		PR:     cmp.Or(p.Number, p.PullRequest.Number),
		Relist: slices.Contains([]string{"opened", "reopened", "closed"}, p.Action),
	}
}

// parseMergeRequest handles GitLab's "Merge Request Hook" payload.
func parseMergeRequest(body []byte) Event {
	var p struct {
		Kind             string `json:"object_kind"`
		ObjectAttributes struct {
			IID    int    `json:"iid"`
			Action string `json:"action"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Kind != "merge_request" {
		return Event{Relist: true}
	}
	return Event{
		PR:     p.ObjectAttributes.IID,
		Relist: slices.Contains([]string{"open", "reopen", "close", "merge"}, p.ObjectAttributes.Action),
	}
}

// parseStatus pulls the head commit SHA from a CI-status event — GitHub
// status/check_run/check_suite and Forgejo/Gitea status share enough shape that
// one parser covers them: the SHA lives at .sha, .check_run.head_sha,
// .check_suite.head_sha, or .commit.{sha,id}. The caller maps the SHA to the
// open PR with that head and refreshes only its check rollup; an opaque payload
// (no SHA) degrades to a relist, which is always safe.
func parseStatus(body []byte) Event {
	var p struct {
		SHA      string `json:"sha"`
		CheckRun struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_run"`
		CheckSuite struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_suite"`
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Event{Relist: true}
	}
	sha := cmp.Or(p.SHA, p.CheckRun.HeadSHA, p.CheckSuite.HeadSHA, p.Commit.SHA, p.Commit.ID)
	if sha == "" {
		return Event{Relist: true}
	}
	return Event{HeadSHA: sha}
}

// parseGitLab routes GitLab payloads by object_kind: a merge request affects the
// open set (parseMergeRequest); a pipeline or job (build) carries the commit SHA
// whose PR's checks we refresh; anything else relists.
func parseGitLab(body []byte) Event {
	var probe struct {
		Kind string `json:"object_kind"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return Event{Relist: true}
	}
	switch probe.Kind {
	case "merge_request":
		return parseMergeRequest(body)
	case "pipeline":
		var p struct {
			ObjectAttributes struct {
				SHA string `json:"sha"`
			} `json:"object_attributes"`
		}
		if json.Unmarshal(body, &p) == nil && p.ObjectAttributes.SHA != "" {
			return Event{HeadSHA: p.ObjectAttributes.SHA}
		}
	case "build": // GitLab's Job Hook
		var p struct {
			SHA    string `json:"sha"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if json.Unmarshal(body, &p) == nil {
			if sha := cmp.Or(p.SHA, p.Commit.SHA); sha != "" {
				return Event{HeadSHA: sha}
			}
		}
	}
	return Event{Relist: true}
}
