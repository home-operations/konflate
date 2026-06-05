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
}

// Parse extracts the affected PR from a verified webhook body. Anything it
// cannot confidently interpret yields Relist (a full re-list), which is always
// safe — at worst it does more work than necessary.
func Parse(forge config.ForgeKind, header http.Header, body []byte) Event {
	body = unwrapFormPayload(header, body)
	switch forge {
	case config.ForgeGitHub:
		return parsePullRequest(header.Get("X-GitHub-Event") == "pull_request", body)
	case config.ForgeForgejo:
		return parsePullRequest(header.Get("X-Gitea-Event") == "pull_request", body)
	case config.ForgeGitLab:
		return parseMergeRequest(body)
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
