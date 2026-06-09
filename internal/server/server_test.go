package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/gitclone"
	"github.com/home-operations/konflate/internal/prfilter"
)

// --- fakes ---------------------------------------------------------------

type fakeProvider struct {
	mu      sync.Mutex
	prs     []api.PR       // the open list ListPRs returns
	details map[int]api.PR // GetPR overrides (e.g. a departed PR's merged/closed state)
	listErr error
}

func (f *fakeProvider) ListPRs(context.Context) ([]api.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prs, f.listErr
}

func (f *fakeProvider) GetPR(_ context.Context, n int) (api.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.details[n]; ok {
		return d, nil
	}
	for _, pr := range f.prs {
		if pr.Number == n {
			return pr, nil
		}
	}
	return api.PR{}, io.EOF
}

// setPRs swaps the open-PR set the forge reports (simulates a PR opening or
// merging between refreshes).
func (f *fakeProvider) setPRs(prs ...api.PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = prs
}

// setDetail registers what GetPR returns for a PR number — used to give a
// departed PR a merged (kept) or closed-unmerged (dropped) verdict.
func (f *fakeProvider) setDetail(pr api.PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.details == nil {
		f.details = map[int]api.PR{}
	}
	f.details[pr.Number] = pr
}

type fakeEngine struct {
	fn func(api.PR) (api.DiffResult, error)
}

func (f *fakeEngine) Diff(_ context.Context, pr api.PR) (api.DiffResult, error) { return f.fn(pr) }

func okEngine() *fakeEngine {
	return &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{PRNumber: pr.Number, HeadSHA: "abc123"}, nil
	}}
}

// --- harness -------------------------------------------------------------

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ghCfg(token string) *config.Config {
	return &config.Config{
		Token:              token,
		MaxDiffConcurrency: 2,
		Forge:              config.ForgeURI{Kind: config.ForgeGitHub, RepoPath: "acme/web", WebBase: "https://github.com"},
	}
}

func newTestServer(t *testing.T, cfg *config.Config, prov *fakeProvider, eng Engine) *Server {
	t.Helper()
	ui := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>konflate</title>")}}
	s := New(cfg, prov, eng, ui, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.runCtx = ctx
	s.queue = newQueue(ctx, s.engine.Diff, s.store, s.hub.broadcast, s.reconcileHeadGone, s.metrics, s.log, cfg.MaxDiffConcurrency)
	return s
}

func do(h http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func waitFor(t *testing.T, s *Server, number int) api.DiffEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := s.store.get(number); ok && (env.Status == api.JobReady || env.Status == api.JobError) {
			return env
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("PR %d never reached a terminal status", number)
	return api.DiffEnvelope{}
}

// --- tests ---------------------------------------------------------------

func TestServer_RefreshListAndDiff(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "feat: widget", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123"}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, okEngine())
	h := s.mainHandler()

	s.refreshList(s.runCtx)
	env := waitFor(t, s, 7)
	if env.Status != api.JobReady {
		t.Fatalf("PR 7 status = %q, want ready (err=%q)", env.Status, env.Error)
	}

	rec := do(h, "GET", "/api/prs", nil, nil)
	var list []api.PRStatus
	mustJSON(t, rec, &list)
	if len(list) != 1 || list[0].Number != 7 || list[0].Status != api.JobReady {
		t.Fatalf("list = %+v", list)
	}

	rec = do(h, "GET", "/api/prs/7/diff", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff: got %d, want 200", rec.Code)
	}
	var de api.DiffEnvelope
	mustJSON(t, rec, &de)
	if de.Status != api.JobReady || de.Diff == nil || de.Diff.PRNumber != 7 {
		t.Fatalf("diff envelope = %+v", de)
	}
}

func TestServer_PRFilterExpr(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	prg, err := prfilter.Compile(`pr.baseRef == "main" && !pr.draft`)
	if err != nil {
		t.Fatalf("compile filter: %v", err)
	}
	cfg.PRFilter = prg

	keep := api.PR{Number: 1, Title: "ok", HeadRef: "a", BaseRef: "main", HeadSHA: "s1", Open: true}
	draft := api.PR{Number: 2, Title: "draft", HeadRef: "b", BaseRef: "main", HeadSHA: "s2", Open: true, Draft: true}
	otherBase := api.PR{Number: 3, Title: "dev base", HeadRef: "c", BaseRef: "dev", HeadSHA: "s3", Open: true}
	prov := &fakeProvider{prs: []api.PR{keep, draft, otherBase}}
	s := newTestServer(t, cfg, prov, okEngine())

	// Every open PR is tracked; the ones the expression rejects (a draft, a
	// non-main base) are kept hidden — listed but never rendered — while the
	// admitted one renders.
	s.refreshList(s.runCtx)
	waitFor(t, s, 1)
	list := s.store.list()
	if len(list) != 3 {
		t.Fatalf("all 3 open PRs should be tracked; got %d: %+v", len(list), list)
	}
	byNum := map[int]api.PRStatus{}
	for _, p := range list {
		byNum[p.Number] = p
	}
	if byNum[1].Hidden {
		t.Errorf("#1 satisfies the filter — must not be hidden")
	}
	if !byNum[2].Hidden || !byNum[3].Hidden {
		t.Errorf("#2 (draft) and #3 (non-main base) must be hidden; got #2=%v #3=%v", byNum[2].Hidden, byNum[3].Hidden)
	}
	// Only the admitted PR renders; hidden PRs are never enqueued.
	if byNum[1].Signals == nil {
		t.Errorf("#1 should have rendered (signals present)")
	}
	if byNum[2].Signals != nil || byNum[3].Signals != nil {
		t.Errorf("hidden PRs must not be rendered (no signals): #2=%v #3=%v", byNum[2].Signals, byNum[3].Signals)
	}

	// #1 becomes a draft (still open) → it now fails the expression and flips to
	// hidden, staying in the list rather than being dropped.
	drafted := keep
	drafted.Draft = true
	prov.setPRs(drafted, draft, otherBase)
	s.refreshList(s.runCtx)
	list = s.store.list()
	if len(list) != 3 {
		t.Fatalf("hidden PRs stay listed; got %d: %+v", len(list), list)
	}
	for _, p := range list {
		if !p.Hidden {
			t.Errorf("#%d should be hidden now (all three fail the filter)", p.Number)
		}
	}
}

func TestServer_Summary(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "feat: widget", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123"}
	// An engine that renders a full diff — resources, tree, chroma CSS, and the
	// headline facts — so we can assert the summary endpoint keeps the latter and
	// drops the former.
	rich := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{
			PRNumber:  pr.Number,
			HeadSHA:   "abc123",
			Summary:   api.DiffSummary{Changed: 2, Added: 1, Removed: 1},
			Impact:    api.Impact{Resources: 4, Parents: 2, CRDs: 1},
			Warnings:  []api.Warning{{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api", Detail: "scaled to zero"}},
			Images:    []api.ImageChange{{Name: "ghcr.io/app", From: "v1", To: "v2"}},
			ChromaCSS: ".chroma{}",
			Tree:      []api.DiffTreeParent{{Label: "HelmRelease app", Kinds: []api.DiffTreeKind{{Kind: "Deployment", Items: []api.DiffTreeItem{{ID: "r0", Name: "web/api", Status: "changed", Add: 1, Del: 1}}}}}},
			Resources: []api.DiffResource{{ID: "r0", Title: "Deployment web/api", Kind: "Deployment", Name: "web/api", Status: "changed"}},
		}, nil
	}}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, rich)
	h := s.mainHandler()
	s.refreshList(s.runCtx)
	waitFor(t, s, 7)

	// Summary keeps the headline facts but drops the heavy per-resource render.
	rec := do(h, "GET", "/api/prs/7/summary", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: got %d, want 200", rec.Code)
	}
	var sum api.DiffEnvelope
	mustJSON(t, rec, &sum)
	if sum.Diff == nil {
		t.Fatal("summary envelope has no diff")
	}
	if len(sum.Diff.Resources) != 0 || len(sum.Diff.Tree) != 0 || sum.Diff.ChromaCSS != "" {
		t.Errorf("summary should drop resources/tree/chroma; got %d resources, %d tree, css=%q",
			len(sum.Diff.Resources), len(sum.Diff.Tree), sum.Diff.ChromaCSS)
	}
	if sum.Diff.Summary.Changed != 2 || sum.Diff.Impact.Resources != 4 || len(sum.Diff.Warnings) != 1 || len(sum.Diff.Images) != 1 {
		t.Errorf("summary dropped headline facts: %+v", sum.Diff)
	}

	// The full diff endpoint is unaffected — the cached render still has its
	// resources and tree (the summary copy didn't mutate the cache).
	var full api.DiffEnvelope
	mustJSON(t, do(h, "GET", "/api/prs/7/diff", nil, nil), &full)
	if full.Diff == nil || len(full.Diff.Resources) != 1 || len(full.Diff.Tree) != 1 {
		t.Errorf("diff endpoint should keep the full render; got %+v", full.Diff)
	}
}

func TestServer_SummaryMarkdown(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "feat: widget", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123"}
	rich := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{
			PRNumber: pr.Number, HeadSHA: "abc123",
			Summary:  api.DiffSummary{Changed: 1},
			Impact:   api.Impact{Resources: 1},
			Warnings: []api.Warning{{Level: api.LevelCaution, Rule: "replicas-zero", Resource: "Deployment web/api", Detail: "scaled to zero"}},
		}, nil
	}}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, rich)
	h := s.mainHandler()
	s.refreshList(s.runCtx)
	waitFor(t, s, 7)

	// Accept: text/markdown → a paste-ready block. The instance is GitHub, so it
	// emits admonitions with no ?forge param, and the review URL is derived from
	// the request host.
	rec := do(h, "GET", "/api/prs/7/summary", nil, map[string]string{"Accept": "text/markdown"})
	if rec.Code != http.StatusOK {
		t.Fatalf("summary md: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type = %q, want text/markdown", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"konflate — summary", "> [!NOTE]", "> [!CAUTION]", "`Deployment web/api`", "/#/pr/7"} {
		if !strings.Contains(body, want) {
			t.Errorf("markdown body missing %q\n%s", want, body)
		}
	}

	// ?forge=gitlab forces the plain (non-admonition) flavour.
	plain := do(h, "GET", "/api/prs/7/summary?forge=gitlab", nil, map[string]string{"Accept": "text/markdown"}).Body.String()
	if strings.Contains(plain, "[!CAUTION]") {
		t.Errorf("?forge=gitlab should not emit GitHub admonitions:\n%s", plain)
	}
	if !strings.Contains(plain, "**⚠ Caution**") {
		t.Errorf("plain flavour missing the cautions heading:\n%s", plain)
	}

	// Default (no Accept) is JSON, unchanged, now carrying reviewUrl.
	var env api.DiffEnvelope
	mustJSON(t, do(h, "GET", "/api/prs/7/summary", nil, nil), &env)
	if env.Diff == nil || !strings.HasSuffix(env.ReviewURL, "/#/pr/7") {
		t.Errorf("json summary reviewUrl = %q (diff set: %v)", env.ReviewURL, env.Diff != nil)
	}
}

func TestServer_SummaryMarkdownRetryAfter(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 9, Title: "wip", HeadRef: "wip", BaseRef: "main", HeadSHA: "deadbeef"}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, okEngine())
	h := s.mainHandler()
	// Record the PR without rendering it → status pending (upsertPR doesn't enqueue).
	s.store.upsertPR(pr, false)

	// Markdown while still rendering → 503 + Retry-After, so `curl --retry` retries
	// (a 202 is a success curl wouldn't retry).
	rec := do(h, "GET", "/api/prs/9/summary", nil, map[string]string{"Accept": "text/markdown"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("markdown while rendering: got %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a still-rendering 503 must carry a Retry-After header")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type = %q, want text/markdown", ct)
	}

	// JSON while rendering stays 202 — the SPA reads it as "still loading", not an error.
	rec = do(h, "GET", "/api/prs/9/summary", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("json while rendering: got %d, want 202", rec.Code)
	}
}

func TestServer_RenderStatusHeader(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		switch pr.Number {
		case 2: // rendered, but a resource failed
			return api.DiffResult{PRNumber: 2, HeadSHA: pr.HeadSHA,
				Failures: []api.RenderFailure{{Parent: "HelmRelease media/plex", Message: "values schema"}}}, nil
		case 3: // the render itself errored
			return api.DiffResult{}, fmt.Errorf("clone failed")
		default: // clean
			return api.DiffResult{PRNumber: pr.Number, HeadSHA: pr.HeadSHA}, nil
		}
	}}
	prs := []api.PR{
		{Number: 1, HeadRef: "a", BaseRef: "main", HeadSHA: "s1", Open: true},
		{Number: 2, HeadRef: "b", BaseRef: "main", HeadSHA: "s2", Open: true},
		{Number: 3, HeadRef: "c", BaseRef: "main", HeadSHA: "s3", Open: true},
	}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: prs}, eng)
	h := s.mainHandler()
	s.refreshList(s.runCtx)
	waitFor(t, s, 1)
	waitFor(t, s, 2)
	waitFor(t, s, 3)
	// #4 is recorded but never rendered → still pending.
	s.store.upsertPR(api.PR{Number: 4, HeadRef: "d", BaseRef: "main", HeadSHA: "s4", Open: true}, false)

	// The verdict header lets a CI gate pass/fail off the same request it fetches
	// the comment with — no second JSON call.
	for n, want := range map[int]string{1: "ok", 2: "failures", 3: "error", 4: "pending"} {
		rec := do(h, "GET", fmt.Sprintf("/api/prs/%d/summary", n), nil, map[string]string{"Accept": "text/markdown"})
		if got := rec.Header().Get(renderStatusHeader); got != want {
			t.Errorf("PR #%d: %s = %q, want %q", n, renderStatusHeader, got, want)
		}
	}
	// Present on the JSON path too (not just Markdown).
	if got := do(h, "GET", "/api/prs/2/summary", nil, nil).Header().Get(renderStatusHeader); got != "failures" {
		t.Errorf("JSON path: %s = %q, want failures", renderStatusHeader, got)
	}
}

func TestServer_AvatarProxy(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.mainHandler()

	// /api/prs rewrites a raw forge avatar URL into a signed same-origin path.
	s.store.upsertPR(api.PR{Number: 7, Open: true, AuthorAvatar: "https://avatars.example/u/octo.png"}, false)
	rec := do(h, "GET", "/api/prs", nil, nil)
	var list []api.PRStatus
	mustJSON(t, rec, &list)
	if len(list) != 1 || !strings.HasPrefix(list[0].AuthorAvatar, "/api/avatar?u=") {
		t.Fatalf("list avatar not proxied: %+v", list)
	}

	// A tampered signature is rejected — the proxy only fetches URLs it signed,
	// so it can't be turned into an open SSRF relay.
	if rec := do(h, "GET", list[0].AuthorAvatar+"deadbeef", nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("tampered signature: got %d, want 403", rec.Code)
	}
	// A correctly-signed but non-https URL is refused before any fetch.
	if rec := do(h, "GET", s.avatarProxyPath("http://avatars.example/x.png"), nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("http avatar URL: got %d, want 400", rec.Code)
	}
}

func forgeCfg(kind config.ForgeKind, repo string) *config.Config {
	return &config.Config{Forge: config.ForgeURI{Kind: kind, RepoPath: repo}}
}

func withMerge(cfg *config.Config, tmpl string) *config.Config {
	cfg.MergeCommand = tmpl
	return cfg
}

func TestMergeCommandRendering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  *config.Config
		pr   api.PR
		want string
	}{
		{"github default", forgeCfg(config.ForgeGitHub, "acme/web"), api.PR{Number: 42, Open: true}, "gh pr merge 42 --repo acme/web"},
		{"gitlab default uses glab", forgeCfg(config.ForgeGitLab, "grp/app"), api.PR{Number: 9, Open: true}, "glab mr merge 9 --repo grp/app"},
		{"forgejo default uses tea", forgeCfg(config.ForgeForgejo, "me/ops"), api.PR{Number: 9, Open: true}, "tea pr merge 9 --repo me/ops"},
		{"operator override", withMerge(forgeCfg(config.ForgeGitHub, "acme/web"), "gh pr merge {{.Number}} --repo {{.Repo}} --squash --delete-branch"), api.PR{Number: 42, Open: true}, "gh pr merge 42 --repo acme/web --squash --delete-branch"},
		{"closed PR has no command", forgeCfg(config.ForgeGitHub, "acme/web"), api.PR{Number: 42, Open: false}, ""},
		// Only {{.Number}} and {{.Repo}} are in scope; referencing an attacker-
		// controlled field (or any typo) fails closed rather than rendering it.
		{"unknown field fails closed", withMerge(forgeCfg(config.ForgeGitHub, "acme/web"), "gh pr merge {{.Number}} {{.Branch}}"), api.PR{Number: 42, Open: true}, ""},
		{"invalid template disables the command", withMerge(forgeCfg(config.ForgeGitHub, "acme/web"), "gh pr merge {{.Number"), api.PR{Number: 42, Open: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{cfg: tc.cfg, mergeTmpl: newMergeTemplate(tc.cfg, discardLog())}
			if got := s.mergeCommand(tc.pr); got != tc.want {
				t.Errorf("mergeCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServer_MergeCommandEndpoints(t *testing.T) {
	t.Parallel()
	cfg := withMerge(ghCfg("tok"), "gh pr merge {{.Number}} --repo {{.Repo}} --squash")
	s := newTestServer(t, cfg, &fakeProvider{}, okEngine())
	h := s.mainHandler()
	s.store.upsertPR(api.PR{Number: 7, Open: true}, false)

	const want = "gh pr merge 7 --repo acme/web --squash"
	var list []api.PRStatus
	mustJSON(t, do(h, "GET", "/api/prs", nil, nil), &list)
	if len(list) != 1 || list[0].MergeCommand != want {
		t.Fatalf("list merge command = %+v, want %q", list, want)
	}
	var env api.DiffEnvelope
	mustJSON(t, do(h, "GET", "/api/prs/7/diff", nil, nil), &env)
	if env.MergeCommand != want {
		t.Errorf("diff merge command = %q, want %q", env.MergeCommand, want)
	}

	// Once merged, neither endpoint offers a command. Decode into a fresh slice:
	// the response omits the empty field (omitempty), and json.Unmarshal won't
	// clear a reused element's stale value.
	s.store.markClosed(7, s.store.now())
	var merged []api.PRStatus
	mustJSON(t, do(h, "GET", "/api/prs", nil, nil), &merged)
	if merged[0].MergeCommand != "" {
		t.Errorf("merged PR list merge command = %q, want empty", merged[0].MergeCommand)
	}
}

func TestUIHandler_CacheControl(t *testing.T) {
	t.Parallel()
	ui := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<!doctype html><title>konflate</title>")},
		"favicon.svg":          &fstest.MapFile{Data: []byte("<svg/>")},
		"assets/app-9f8e7d.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	h := (&Server{ui: ui}).uiHandler()

	cases := []struct{ path, want string }{
		// Content-hashed bundle: cache hard, never revalidate (the URL changes on
		// the next build, so a stale file is never requested).
		{"/assets/app-9f8e7d.js", "public, max-age=31536000, immutable"},
		// The entry point and the unhashed favicon must always revalidate so a
		// redeploy is picked up immediately.
		{"/", "no-cache"},
		{"/favicon.svg", "no-cache"},
	}
	for _, tc := range cases {
		rec := do(h, "GET", tc.path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestServer_ClosedPRsMergedKeptAbandonedDropped(t *testing.T) {
	t.Parallel()
	open := func(n int, ref string) api.PR {
		return api.PR{Number: n, Open: true, State: "open", HeadRef: ref, BaseRef: "main"}
	}
	prov := &fakeProvider{}
	prov.setPRs(open(1, "a"), open(2, "b"), open(3, "c"))
	s := newTestServer(t, ghCfg("tok"), prov, okEngine())
	h := s.mainHandler()

	s.refreshList(s.runCtx)
	waitFor(t, s, 1)
	waitFor(t, s, 2)
	waitFor(t, s, 3)

	// #2 merges, #3 is closed without merging; the forge now lists only #1.
	prov.setPRs(open(1, "a"))
	prov.setDetail(api.PR{Number: 2, State: "closed", Merged: true, HeadRef: "b", BaseRef: "main"})
	prov.setDetail(api.PR{Number: 3, State: "closed", Merged: false, HeadRef: "c", BaseRef: "main"})
	s.refreshList(s.runCtx)

	// Wait for the reconcile: #3 dropped, #2 frozen as merged.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, has3 := s.store.get(3)
		env2, has2 := s.store.get(2)
		if !has3 && has2 && env2.PR.Merged {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	rec := do(h, "GET", "/api/prs", nil, nil)
	var list []api.PRStatus
	mustJSON(t, rec, &list)
	byNum := map[int]api.PRStatus{}
	for _, p := range list {
		byNum[p.Number] = p
	}
	if _, ok := byNum[3]; ok {
		t.Fatalf("abandoned PR #3 should be dropped: %+v", list)
	}
	p2, ok := byNum[2]
	if !ok || !p2.Merged || p2.Open || p2.ClosedAt == nil {
		t.Fatalf("merged PR #2 should be retained as closed with closedAt set: %+v", p2)
	}
	if p2.Signals == nil {
		t.Errorf("merged PR #2 should keep its frozen diff signals")
	}
	if p1 := byNum[1]; !p1.Open {
		t.Errorf("open PR #1 should stay open: %+v", p1)
	}
}

func TestStore_PruneClosed(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st := newStore()
	for i := 1; i <= 4; i++ { // #i merged at base+i hours (newer = higher number)
		st.upsertPR(api.PR{Number: i, Open: true}, false)
		st.markClosed(i, base.Add(time.Duration(i)*time.Hour))
	}
	now := base.Add(10 * time.Hour)

	// Count cap of 2 keeps the two most recent (#4, #3); prunes #1, #2.
	removed := st.pruneClosed(now, 0, 2)
	slices.Sort(removed)
	if !slices.Equal(removed, []int{1, 2}) {
		t.Fatalf("count prune removed %v, want [1 2]", removed)
	}
	// Age cap: #3 closed at +3h (age 7h) is too old; #4 at +4h (age 6h) survives.
	removed = st.pruneClosed(now, 6*time.Hour+30*time.Minute, 0)
	if !slices.Equal(removed, []int{3}) {
		t.Fatalf("age prune removed %v, want [3]", removed)
	}
	if _, ok := st.get(4); !ok {
		t.Error("#4 should survive both caps")
	}
}

func TestServer_Meta(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("secret-token")
	cfg.WebhookSecret = "supersecret"
	cfg.RefreshInterval = 10 * time.Minute
	s := newTestServer(t, cfg, &fakeProvider{}, okEngine())
	s.Version = "1.2.3"

	rec := do(s.mainHandler(), "GET", "/api/meta", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("meta: got %d, want 200", rec.Code)
	}
	var m api.Meta
	mustJSON(t, rec, &m)
	if m.Forge != "github" || m.Repo != "acme/web" || m.RefreshIntervalSeconds != 600 {
		t.Errorf("meta = %+v, want github/acme/web/600s", m)
	}
	if m.RepoURL != "https://github.com/acme/web" || m.Version != "1.2.3" {
		t.Errorf("meta = %+v, want repoUrl https://github.com/acme/web and version 1.2.3", m)
	}
	// /api/meta must never leak a secret.
	if body := rec.Body.String(); strings.Contains(body, "supersecret") || strings.Contains(body, "secret-token") {
		t.Errorf("meta response leaked a secret: %s", body)
	}
}

func TestStaleJitter_DeterministicAndBounded(t *testing.T) {
	t.Parallel()
	interval := 30 * time.Minute
	bound := interval / 4 // |offset| < interval/4
	for _, pr := range []int{1, 2, 7, 42, 999, 123456} {
		if a, b := staleJitter(pr, interval), staleJitter(pr, interval); a != b {
			t.Errorf("staleJitter(%d) not deterministic: %v vs %v", pr, a, b)
		}
		if j := staleJitter(pr, interval); j < -bound || j >= bound {
			t.Errorf("staleJitter(%d) = %v, want within [-%v, %v)", pr, j, bound, bound)
		}
	}
}

func TestStore_StalePRsAreJittered(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st := newStore()
	st.now = func() time.Time { return base }
	const n = 40
	interval := 60 * time.Minute
	for i := 1; i <= n; i++ { // all rendered at the same instant (the startup batch)
		st.upsertPR(api.PR{Number: i, Open: true}, false)
		st.setResult(i, api.DiffResult{PRNumber: i})
	}
	count := func(d time.Duration) int { return len(st.stalePRs(base.Add(d), interval)) }

	if c := count(interval / 2); c != 0 {
		t.Errorf("at 0.5·interval: %d stale, want 0 (before the earliest jittered deadline)", c)
	}
	if c := count(interval); c == 0 || c == n {
		t.Errorf("at the nominal interval: %d stale — jitter should split the herd, not fire all-at-once", c)
	}
	if c := count(2 * interval); c != n {
		t.Errorf("at 2·interval: %d stale, want all %d", c, n)
	}
}

func TestServer_RefreshStaleReRendersOpenOnly(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	renders := map[int]int{}
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		mu.Lock()
		renders[pr.Number]++
		mu.Unlock()
		return api.DiffResult{PRNumber: pr.Number}, nil
	}}
	cfg := ghCfg("tok")
	cfg.RefreshInterval = 30 * time.Minute
	prov := &fakeProvider{}
	prov.setPRs(api.PR{Number: 1, Open: true, State: "open", HeadRef: "a"})
	s := newTestServer(t, cfg, prov, eng)

	// Initial render of the open PR.
	s.refreshList(s.runCtx)
	waitFor(t, s, 1)
	// A merged PR that has already rendered, then frozen onto the shelf.
	s.store.upsertPR(api.PR{Number: 2, Open: true}, false)
	s.store.setResult(2, api.DiffResult{PRNumber: 2})
	s.store.markClosed(2, s.store.now())

	// Just rendered → nothing stale yet.
	s.refreshStale(s.store.now())
	// An interval later: the open PR is stale and re-renders; the merged one is
	// frozen and must not, even though it's just as old.
	s.refreshStale(s.store.now().Add(time.Hour))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := renders[1] == 2
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if renders[1] != 2 {
		t.Errorf("open PR rendered %d times, want 2 (initial + one stale refresh)", renders[1])
	}
	if renders[2] != 0 {
		t.Errorf("merged PR rendered %d times, want 0 (frozen)", renders[2])
	}
}

// TestStore_ClosedJobRejectsLateWrites verifies the merged shelf is frozen: a
// render still in flight when the PR merged cannot drag the shelved PR into a
// running/errored state or wipe its frozen diff.
func TestStore_ClosedJobRejectsLateWrites(t *testing.T) {
	t.Parallel()
	st := newStore()
	st.upsertPR(api.PR{Number: 1, Open: true}, false)
	st.setResult(1, api.DiffResult{PRNumber: 1})
	st.markClosed(1, st.now())

	// The stale in-flight render finishes after the PR was shelved.
	st.setStatus(api.PR{Number: 1}, api.JobRunning)
	if st.failRender(1, "engine: clone PR #1: gitclone: head ref no longer exists on remote") {
		t.Error("failRender reported keeping a diff for a shelved PR")
	}

	env, _ := st.get(1)
	if env.Status != api.JobReady {
		t.Errorf("shelved PR status = %q, want ready (late writes ignored)", env.Status)
	}
	if env.Error != "" || env.RefreshError != "" {
		t.Errorf("shelved PR error = %q/%q, want empty (late failRender ignored)", env.Error, env.RefreshError)
	}
	if env.Diff == nil {
		t.Error("shelved PR lost its frozen diff to a late write")
	}
}

func TestStore_FailRenderKeepsLastGoodDiff(t *testing.T) {
	t.Parallel()
	st := newStore()
	st.upsertPR(api.PR{Number: 1, Open: true}, false)

	// Never rendered → a failure flips it to the error state.
	if st.failRender(1, "boom") {
		t.Error("failRender kept a diff for a never-rendered PR")
	}
	if env, _ := st.get(1); env.Status != api.JobError || env.Error == "" {
		t.Fatalf("never-rendered failure: status=%q err=%q, want error", env.Status, env.Error)
	}

	// After a good render, a failure keeps the diff and flags refreshError
	// instead of clobbering it (a transient forge/git outage must not wipe it).
	st.setResult(1, api.DiffResult{PRNumber: 1})
	if !st.failRender(1, "forge down") {
		t.Error("failRender dropped a good diff on a transient failure")
	}
	env, _ := st.get(1)
	if env.Status != api.JobReady || env.Diff == nil {
		t.Fatalf("kept-render: status=%q diff=%v, want ready+diff", env.Status, env.Diff)
	}
	if env.RefreshError != "forge down" {
		t.Errorf("refreshError = %q, want %q", env.RefreshError, "forge down")
	}
	// A later success clears the refresh-error flag.
	st.setResult(1, api.DiffResult{PRNumber: 1})
	if env, _ := st.get(1); env.RefreshError != "" {
		t.Errorf("refreshError = %q after success, want empty", env.RefreshError)
	}
}

// TestServer_HeadGoneMidRenderShelvesNotFails is the end-to-end of the merged-PR
// race: a PR is enqueued while open, merges (branch deleted) before its render
// runs, and the render fails to fetch the head ref. The PR must land on the
// merged shelf, never shown as a failed render.
func TestServer_HeadGoneMidRenderShelvesNotFails(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{}, fmt.Errorf("engine: clone PR #%d: %w", pr.Number, gitclone.ErrHeadRefGone)
	}}
	prov := &fakeProvider{}
	// By the time konflate reconciles, the forge reports the PR merged.
	prov.setDetail(api.PR{Number: 7, State: "merged", Merged: true, HeadRef: "renovate/x", BaseRef: "main"})
	s := newTestServer(t, ghCfg("tok"), prov, eng)

	s.store.upsertPR(api.PR{Number: 7, Open: true, HeadRef: "renovate/x", BaseRef: "main"}, false)
	s.queue.enqueue(api.PR{Number: 7, Open: true, HeadRef: "renovate/x", BaseRef: "main"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := s.store.get(7); ok && env.PR.Merged {
			if env.Status == api.JobError {
				t.Fatalf("merged-mid-render PR was marked errored: %q", env.Error)
			}
			return // reconciled onto the shelf, not failed
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("PR 7 was never reconciled onto the merged shelf after a head-gone render")
}

func TestServer_SignalsInList(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{
			PRNumber:  pr.Number,
			Resources: []api.DiffResource{{ID: "r0"}, {ID: "r1"}},
			Images:    []api.ImageChange{{Name: "ghcr.io/x"}},
			Failures:  []api.RenderFailure{{Parent: "HR a/b"}},
			Warnings:  []api.Warning{{Level: "danger"}, {Level: "caution"}, {Level: "caution"}},
		}, nil
	}}
	pr := api.PR{Number: 3, HeadRef: "f", BaseRef: "main"}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, eng)
	h := s.mainHandler()

	s.refreshList(s.runCtx)
	waitFor(t, s, 3)

	var list []api.PRStatus
	mustJSON(t, do(h, "GET", "/api/prs", nil, nil), &list)
	if len(list) != 1 || list[0].Signals == nil {
		t.Fatalf("expected signals on the ready PR: %+v", list)
	}
	got := *list[0].Signals
	want := api.Signals{Resources: 2, Caution: 3, Images: 1, Failures: 1}
	if got != want {
		t.Errorf("signals = %+v, want %+v", got, want)
	}
}

func TestServer_DiffUnknownAndBadNumber(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.mainHandler()

	if rec := do(h, "GET", "/api/prs/999/diff", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown PR: got %d, want 404", rec.Code)
	}
	if rec := do(h, "GET", "/api/prs/notanumber/diff", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad number: got %d, want 400", rec.Code)
	}
}

func TestServer_InboundGatedBySecretsNotToken(t *testing.T) {
	t.Parallel()

	// No secrets (and no token): inbound endpoints are off; there is no manual
	// refresh endpoint at all.
	off := newTestServer(t, ghCfg(""), &fakeProvider{}, okEngine()).mainHandler()
	for _, path := range []string{"/hooks", "/api/prs/7/refresh"} {
		if rec := do(off, "POST", path, nil, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("POST %s without its secret: got %d, want 501", path, rec.Code)
		}
	}
	// There is no manual-refresh trigger: POST /api/refresh isn't routed, so the
	// mux rejects the method rather than accepting a refresh.
	if rec := do(off, "POST", "/api/refresh", nil, nil); rec.Code == http.StatusAccepted {
		t.Errorf("POST /api/refresh should not trigger a refresh: got %d", rec.Code)
	}

	// Secrets set but NO forge token (decoupled): the endpoints are enabled — the
	// secret, not the token, is what gates them.
	cfg := ghCfg("") // anonymous-equivalent: no forge token
	cfg.WebhookSecret = "wh"
	cfg.PushToken = "pt"
	on := newTestServer(t, cfg, &fakeProvider{}, okEngine()).mainHandler()
	// Push without the bearer token → 401 (enabled, but unauthorized), not 501.
	if rec := do(on, "POST", "/api/prs/7/refresh", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("push enabled by its token (no forge token): got %d, want 401", rec.Code)
	}
	// Webhook with a bad signature → 401 (enabled), not 501.
	if rec := do(on, "POST", "/hooks", strings.NewReader("{}"), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("webhook enabled by its secret (no forge token): got %d, want 401", rec.Code)
	}
}

func TestServer_PushAuth(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	cfg.PushToken = "s3cret"
	pr := api.PR{Number: 9, Open: true, HeadRef: "feat", BaseRef: "main"}
	s := newTestServer(t, cfg, &fakeProvider{prs: []api.PR{pr}}, okEngine())
	h := s.mainHandler()

	if rec := do(h, "POST", "/api/prs/9/refresh", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	if rec := do(h, "POST", "/api/prs/9/refresh", nil, map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", rec.Code)
	}
	rec := do(h, "POST", "/api/prs/9/refresh", nil, map[string]string{"Authorization": "Bearer s3cret"})
	if rec.Code != http.StatusAccepted {
		t.Errorf("correct token: got %d, want 202", rec.Code)
	}
	waitFor(t, s, 9)
}

func TestServer_Webhook(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	// GitHub's published HMAC-SHA256 vector.
	cfg.WebhookSecret = "It's a Secret to Everybody"
	const body = "Hello, World!"
	const sig = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	s := newTestServer(t, cfg, &fakeProvider{}, okEngine())
	h := s.mainHandler()

	rec := do(h, "POST", "/hooks", strings.NewReader(body), map[string]string{"X-Hub-Signature-256": sig})
	if rec.Code != http.StatusAccepted {
		t.Errorf("valid signature: got %d, want 202", rec.Code)
	}
	rec = do(h, "POST", "/hooks", strings.NewReader(body), map[string]string{"X-Hub-Signature-256": "sha256=deadbeef"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: got %d, want 401", rec.Code)
	}
}

func TestServer_WebhookPerPR(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	cfg.WebhookSecret = "shh"
	// The forge knows two PRs; a content webhook for #5 must touch only #5 and
	// must not re-list (which would pull in #6).
	prs := []api.PR{{Number: 5, Open: true, HeadRef: "f5", BaseRef: "main"}, {Number: 6, Open: true, HeadRef: "f6", BaseRef: "main"}}
	s := newTestServer(t, cfg, &fakeProvider{prs: prs}, okEngine())
	h := s.mainHandler()

	body := `{"action":"synchronize","number":5,"pull_request":{"number":5}}`
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := do(h, "POST", "/hooks", strings.NewReader(body), map[string]string{
		"X-GitHub-Event":      "pull_request",
		"X-Hub-Signature-256": sig,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook: got %d, want 202", rec.Code)
	}

	waitFor(t, s, 5) // #5 was rendered
	if _, ok := s.store.get(6); ok {
		t.Error("webhook re-listed PRs (#6 appeared); expected a per-PR refresh of #5 only")
	}
}

func TestServer_WebhookFormEncoded(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	cfg.WebhookSecret = "shh"
	s := newTestServer(t, cfg, &fakeProvider{prs: []api.PR{{Number: 5, Open: true, HeadRef: "f5", BaseRef: "main"}}}, okEngine())
	h := s.mainHandler()

	// GitHub's default content type wraps the JSON in a `payload=` form field; the
	// signature is over that raw form body. The per-PR path must still run rather
	// than degrade to a full re-list.
	body := "payload=" + url.QueryEscape(`{"action":"synchronize","number":5,"pull_request":{"number":5}}`)
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := do(h, "POST", "/hooks", strings.NewReader(body), map[string]string{
		"X-GitHub-Event":      "pull_request",
		"Content-Type":        "application/x-www-form-urlencoded",
		"X-Hub-Signature-256": sig,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook: got %d, want 202", rec.Code)
	}
	waitFor(t, s, 5) // rendered via the per-PR path despite the form content type
}

func TestServer_HealthAndSecurityHeaders(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.mainHandler()

	rec := do(h, "GET", "/healthz", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("healthz: %d %q", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("missing/weak CSP: %q", csp)
	}
}

// TestServer_WebsocketStatusEvents drives a real websocket client against a
// real httptest server and asserts that diff-job status changes stream to it.
func TestServer_WebsocketStatusEvents(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		<-release // hold the render so the client is registered before it finishes
		return api.DiffResult{PRNumber: pr.Number, HeadSHA: "abc"}, nil
	}}
	pr := api.PR{Number: 5, HeadRef: "feat", BaseRef: "main"}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, eng)

	httpSrv := httptest.NewServer(s.mainHandler())
	t.Cleanup(httpSrv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	time.Sleep(50 * time.Millisecond) // let serveWS register the client
	s.queue.enqueue(pr)
	close(release)

	for range 12 {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("ws read: %v", err)
		}
		var ev api.Event
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if ev.Number == 5 && ev.Status == api.JobReady {
			return // success
		}
	}
	t.Fatal("never received a ready event for PR 5 over the websocket")
}

func mustJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %T: %v (body=%s)", v, err, rec.Body.String())
	}
}
