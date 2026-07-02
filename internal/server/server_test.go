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
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/gitclone"
	"github.com/home-operations/konflate/internal/prfilter"
	"github.com/home-operations/konflate/internal/provider"
)

// --- fakes ---------------------------------------------------------------

type fakeProvider struct {
	mu         sync.Mutex
	prs        []api.PR       // the open list ListPRs returns
	details    map[int]api.PR // GetPR overrides (e.g. a departed PR's merged/closed state)
	notFound   map[int]bool   // numbers GetPR reports as deleted (provider.ErrPRNotFound)
	listErr    error
	listHook   func()                  // optional seam invoked at the start of each ListPRs (set before use)
	checksHook func()                  // optional seam invoked at the start of each Checks (set before use)
	checks     map[int]api.CheckRollup // per-PR CI rollup Checks returns (zero value → none)

	listCalls   int // ListPRs invocations (read via callCounts)
	checksCalls int // Checks invocations (read via callCounts)
}

func (f *fakeProvider) Checks(_ context.Context, pr api.PR) (api.CheckRollup, error) {
	if f.checksHook != nil {
		f.checksHook() // outside the lock so a hook may block without wedging the fake
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checksCalls++
	return f.checks[pr.Number], nil
}

func (f *fakeProvider) ListPRs(context.Context) ([]api.PR, error) {
	if f.listHook != nil {
		f.listHook() // outside the lock so a hook may block without wedging the fake
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return f.prs, f.listErr
}

func (f *fakeProvider) GetPR(_ context.Context, n int) (api.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFound[n] {
		return api.PR{}, fmt.Errorf("fake: %w", provider.ErrPRNotFound)
	}
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

// setNotFound makes GetPR(n) report the PR as deleted (provider.ErrPRNotFound).
func (f *fakeProvider) setNotFound(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFound == nil {
		f.notFound = map[int]bool{}
	}
	f.notFound[n] = true
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

// callCounts returns how many times ListPRs and Checks have run.
func (f *fakeProvider) callCounts() (list, checks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls, f.checksCalls
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
	s.runCtx = t.Context()
	s.queue = newQueue(s.runCtx, s.engine.Diff, s.store, s.hub.broadcast, s.reconcileHeadGone, s.metrics, s.log, cfg.MaxDiffConcurrency, nil)
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

// TestServer_WebhookRelistCoalesces verifies a burst of relist triggers collapses
// to one in-flight refreshList plus one trailing run, rather than a ListPRs per
// event — the forge-API-quota fix for a chatty "send everything" webhook.
func TestServer_WebhookRelistCoalesces(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	prov := &fakeProvider{}
	prov.listHook = func() {
		if calls.Add(1) == 1 {
			entered <- struct{}{} // the first relist is now in flight
			<-release             // hold it so a burst can pile up behind it
		}
	}
	s := newTestServer(t, ghCfg("tok"), prov, okEngine())
	go s.relistWorker(t.Context())

	// First trigger: the worker enters ListPRs and blocks there.
	s.requestRelist()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("relist worker never started the first run")
	}

	// A burst arrives while that run is in flight; it must coalesce, not fan out.
	for range 5 {
		s.requestRelist()
	}
	close(release) // let the in-flight run finish; the worker then drains one token

	// Exactly two ListPRs calls: the in-flight run + one coalesced trailing run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let any erroneous extra run surface
	if got := calls.Load(); got != 2 {
		t.Fatalf("ListPRs calls = %d, want 2 (one in-flight + one coalesced trailing run)", got)
	}
}

// TestServer_CheckRefreshCoalesces verifies a burst of check-status webhooks for
// one head SHA collapses to one in-flight Checks poll plus one trailing poll,
// rather than a forge call per event — the fix for a CI cycle (or a Renovate
// batch) delivering dozens of check_run/check_suite deliveries that otherwise fan
// out into a concurrent burst of forge calls.
func TestServer_CheckRefreshCoalesces(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	prov := &fakeProvider{}
	prov.checksHook = func() {
		if calls.Add(1) == 1 {
			entered <- struct{}{} // the first refresh is now in flight
			<-release             // hold it so a burst can pile up behind it
		}
	}
	s := newTestServer(t, ghCfg("tok"), prov, okEngine())
	s.store.upsertPR(api.PR{Number: 7, HeadSHA: sha, Open: true}, false) // so prByHeadSHA(sha) resolves
	go s.checkRefreshWorker(t.Context())

	// First trigger: the worker enters Checks and blocks there.
	s.requestCheckRefresh(sha)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("check-refresh worker never started the first run")
	}

	// A burst for the same head arrives while that refresh is in flight; it must
	// coalesce, not fan out into a Checks call per event.
	for range 5 {
		s.requestCheckRefresh(sha)
	}
	close(release) // let the in-flight refresh finish; the worker then drains one trailing entry

	// Exactly two Checks calls: the in-flight refresh + one coalesced trailing poll.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let any erroneous extra run surface
	if got := calls.Load(); got != 2 {
		t.Fatalf("Checks calls = %d, want 2 (one in-flight + one coalesced trailing poll)", got)
	}
}

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

// TestServer_DiffETagConditional verifies the diff endpoint serves a strong
// validator and honors If-None-Match: a matching conditional request gets a
// bodyless 304 (no re-marshal of the diff), and a new render with different
// content changes the ETag so a stale validator falls back to a full 200.
func TestServer_DiffETagConditional(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.mainHandler()
	s.store.upsertPR(api.PR{Number: 7, Open: true}, false)
	s.store.setResult(7, api.DiffResult{PRNumber: 7, ChromaCSS: ".a{}"})

	rec := do(h, "GET", "/api/prs/7/diff", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first diff: got %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("a ready diff response must carry an ETag")
	}

	rec = do(h, "GET", "/api/prs/7/diff", nil, map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match: got %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 304 must have an empty body, got %d bytes", rec.Body.Len())
	}

	// New render, different content → the validator changes, so the stale one
	// no longer matches and the client gets a full response.
	s.store.setResult(7, api.DiffResult{PRNumber: 7, ChromaCSS: ".b{}"})
	rec = do(h, "GET", "/api/prs/7/diff", nil, map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusOK {
		t.Fatalf("after a content change, a stale validator must get 200, got %d", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Error("the ETag must change when the diff content changes")
	}
}

// TestServer_DiffJSONNotHTMLEscaped verifies the API encoder leaves </>/&
// unescaped (the bodies are application/json + nosniff, so escaping only bloats
// the span-dense diff HTML).
func TestServer_DiffJSONNotHTMLEscaped(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.mainHandler()
	s.store.upsertPR(api.PR{Number: 7, Open: true}, false)
	s.store.setResult(7, api.DiffResult{PRNumber: 7, ChromaCSS: "x < y > z & w"})

	body := do(h, "GET", "/api/prs/7/diff", nil, nil).Body.String()
	if !strings.Contains(body, "x < y > z & w") {
		t.Errorf("API JSON should not HTML-escape diff content; body = %s", body)
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

// TestServer_CheckFilterCatchesFieldTypo verifies the startup smoke-test: an
// expression that references a field by the wrong name compiles (pr is a dynamic
// map, so CEL can't catch it) but checkFilter rejects it, turning a runtime
// "hide every PR" into a fail-fast startup error. A well-formed filter passes.
func TestServer_CheckFilterCatchesFieldTypo(t *testing.T) {
	t.Parallel()
	typo, err := prfilter.Compile(`!pr.isDraft`) // real field is pr.draft
	if err != nil {
		t.Fatalf("the typo should compile (caught only at eval); got %v", err)
	}
	cfg := ghCfg("")
	cfg.PRFilter = typo
	s := newTestServer(t, cfg, &fakeProvider{}, okEngine())
	if err := s.checkFilter(); err == nil {
		t.Fatal("checkFilter must reject a filter referencing a nonexistent field")
	}

	good, err := prfilter.Compile(`!pr.draft && pr.baseRef == "main"`)
	if err != nil {
		t.Fatalf("compile good filter: %v", err)
	}
	s.cfg.PRFilter = good
	if err := s.checkFilter(); err != nil {
		t.Fatalf("checkFilter on a valid filter: %v", err)
	}
}

// TestServer_DropFilteredOnLoadKeepsOnEvalError verifies a filter evaluation
// error never deletes persisted state: a typo'd expression would otherwise wipe
// the recently-merged shelf on restart. A cleanly-excluded PR is still dropped.
func TestServer_DropFilteredOnLoadKeepsOnEvalError(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("")
	typo, err := prfilter.Compile(`pr.isMerged`) // real field is pr.merged
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	cfg.PRFilter = typo
	s := newTestServer(t, cfg, &fakeProvider{}, okEngine())

	// A merged PR on the shelf with a rendered diff — exactly what a filter typo
	// must not destroy.
	s.store.upsertPR(api.PR{Number: 5, Merged: true}, false)
	s.store.setResult(5, api.DiffResult{PRNumber: 5})
	s.store.markClosed(5, s.store.now())

	s.dropFilteredOnLoad()
	if _, ok := s.store.get(5); !ok {
		t.Fatal("a filter eval error deleted persisted state; PR 5 must be kept")
	}

	// A clean exclusion still drops (the function's original job): pr.merged
	// evaluates fine and excludes a non-merged PR.
	good, err := prfilter.Compile(`pr.merged`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	s.cfg.PRFilter = good
	s.store.upsertPR(api.PR{Number: 6, Open: true}, false)
	s.store.setResult(6, api.DiffResult{PRNumber: 6})
	s.dropFilteredOnLoad()
	if _, ok := s.store.get(6); ok {
		t.Fatal("a cleanly-excluded PR should be dropped on load")
	}
	if _, ok := s.store.get(5); !ok {
		t.Fatal("PR 5 (merged, matches pr.merged) must still be kept")
	}
}

// TestServer_HiddenToAllowedRenders verifies a PR that becomes filter-allowed
// without a head push (draft → ready under !pr.draft) is enqueued on the next
// re-list — it would otherwise sit pending forever, since nothing else enqueues
// it in polling mode and the staleness backstop skips never-rendered jobs.
func TestServer_HiddenToAllowedRenders(t *testing.T) {
	t.Parallel()
	cfg := ghCfg("tok")
	prg, err := prfilter.Compile(`!pr.draft`)
	if err != nil {
		t.Fatalf("compile filter: %v", err)
	}
	cfg.PRFilter = prg

	draft := api.PR{Number: 1, Title: "wip", HeadRef: "f", BaseRef: "main", HeadSHA: "s1", Open: true, Draft: true}
	prov := &fakeProvider{prs: []api.PR{draft}}
	s := newTestServer(t, cfg, prov, okEngine())

	// First list: the draft is tracked but hidden, never rendered.
	s.refreshList(s.runCtx)
	if env, _ := s.store.get(1); !env.Hidden {
		t.Fatal("a draft under !pr.draft must be tracked as hidden")
	}

	// Marked ready for review — same head SHA, no push.
	ready := draft
	ready.Draft = false
	prov.setPRs(ready)
	s.refreshList(s.runCtx)

	env := waitFor(t, s, 1) // before the fix this never reached a terminal status
	if env.Status != api.JobReady {
		t.Fatalf("draft→ready (no push) should render; status=%q err=%q", env.Status, env.Error)
	}
	if env.Hidden {
		t.Error("PR 1 should no longer be hidden")
	}
}

func TestServer_RenderForkPRs(t *testing.T) {
	t.Parallel()
	fork := api.PR{Number: 7, Title: "fork PR", HeadRef: "patch", BaseRef: "main", HeadSHA: "s7", Open: true, Fork: true}
	plain := api.PR{Number: 8, Title: "same-repo PR", HeadRef: "feat", BaseRef: "main", HeadSHA: "s8", Open: true}

	// Gate off (the default): the fork is tracked but hidden and never rendered,
	// independent of the filter; the same-repo PR renders.
	off := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{fork, plain}}, okEngine())
	off.refreshList(off.runCtx)
	waitFor(t, off, 8)
	got := map[int]api.PRStatus{}
	for _, p := range off.store.list() {
		got[p.Number] = p
	}
	if !got[7].Hidden || got[7].Signals != nil {
		t.Errorf("a fork must be hidden and unrendered with the gate off; got %+v", got[7])
	}
	if got[8].Hidden {
		t.Errorf("a same-repo PR must not be hidden")
	}

	// Gate on: the fork is admitted and rendered like any other PR.
	cfg := ghCfg("tok")
	cfg.RenderForkPRs = true
	on := newTestServer(t, cfg, &fakeProvider{prs: []api.PR{fork}}, okEngine())
	on.refreshList(on.runCtx)
	waitFor(t, on, 7)
	if list := on.store.list(); len(list) != 1 || list[0].Hidden {
		t.Fatalf("a fork should render (not hidden) with the gate on; got %+v", list)
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
	for _, want := range []string{"konflate — summary", "> [!NOTE]", "> [!WARNING]", "`Deployment web/api`", "/#/pr/7"} {
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

// TestServer_DeletedPRReaped verifies a PR deleted on the forge (GetPR →
// ErrPRNotFound) is removed rather than retried forever — via both the periodic
// reconcileClosed and the queue's reconcileHeadGone.
func TestServer_DeletedPRReaped(t *testing.T) {
	t.Parallel()
	a := api.PR{Number: 1, Open: true, HeadSHA: "s1", BaseRef: "main"}
	b := api.PR{Number: 2, Open: true, HeadSHA: "s2", BaseRef: "main"}
	prov := &fakeProvider{prs: []api.PR{a, b}}
	s := newTestServer(t, ghCfg("tok"), prov, okEngine())
	s.store.upsertPR(a, false)
	s.store.upsertPR(b, false)

	// #1 is deleted: gone from the open list and GetPR now 404s.
	prov.setPRs(b)
	prov.setNotFound(1)
	s.refreshList(s.runCtx) // reconcileClosed must reap #1, not loop on it
	if _, ok := s.store.get(1); ok {
		t.Fatal("reconcileClosed must reap a deleted PR (GetPR not-found)")
	}

	// #2 is then deleted while still tracked; the head-gone path must reap it too.
	prov.setNotFound(2)
	s.reconcileHeadGone(2)
	if _, ok := s.store.get(2); ok {
		t.Fatal("reconcileHeadGone must reap a deleted PR, not loop on the gone head ref")
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
	// rerender 0 ⇒ memo off, so every PR uses the base interval (this test is about
	// the jitter spread, not the re-render memo, which has its own test).
	count := func(d time.Duration) int { return len(st.stalePRs(base.Add(d), interval, 0)) }

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

// TestStore_StalePRsMemoOnUnchangedSHA verifies the re-render memo: an open PR
// whose head SHA is unchanged since its last clean render is held off the base
// interval and re-rendered only on the slower rerender cadence (mutable-source
// drift catch), while a PR whose head advanced still re-renders at the base
// interval.
func TestStore_StalePRsMemoOnUnchangedSHA(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = 30 * time.Minute
	const rerender = 6 * time.Hour

	st := newStore()
	st.now = func() time.Time { return base }
	// #1: result SHA == head SHA → unchanged. #2: head advanced past the rendered
	// SHA (the result is for an older head) → changed.
	st.upsertPR(api.PR{Number: 1, Open: true, HeadSHA: "sha1"}, false)
	st.setResult(1, api.DiffResult{PRNumber: 1, HeadSHA: "sha1"})
	st.upsertPR(api.PR{Number: 2, Open: true, HeadSHA: "new"}, false)
	st.setResult(2, api.DiffResult{PRNumber: 2, HeadSHA: "old"})

	staleAt := func(d time.Duration) map[int]bool {
		m := map[int]bool{}
		for _, pr := range st.stalePRs(base.Add(d), interval, rerender) {
			m[pr.Number] = true
		}
		return m
	}

	// Past the base interval but far inside the 6h re-render window: the changed
	// PR re-renders; the unchanged one is memoized.
	at := staleAt(2 * interval)
	if !at[2] {
		t.Error("changed PR (#2) should be stale at the base interval")
	}
	if at[1] {
		t.Error("unchanged PR (#1) must be memoized within the re-render interval")
	}
	// Past the re-render cadence: the unchanged PR re-renders too (drift catch).
	if !staleAt(2 * rerender)[1] {
		t.Error("unchanged PR (#1) should re-render once past the re-render interval")
	}

	// rerender<=0 disables the memo: the unchanged PR is stale at the base interval.
	if len(st.stalePRs(base.Add(2*interval), interval, 0)) != 2 {
		t.Error("rerender<=0 should disable the memo (both PRs stale at the base interval)")
	}
}

// TestRefreshCadence pins the refresh loop's enable/cadence decision: <=0
// disables the periodic refresh (so KONFLATE_REFRESH_INTERVAL=0 means "inbound
// triggers only" rather than the old 1s hot loop), and a positive interval is
// capped at 2m so a long interval still wakes often enough for per-PR staleness.
func TestRefreshCadence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		interval    time.Duration
		wantCadence time.Duration
		wantEnabled bool
	}{
		{"zero disables", 0, 0, false},
		{"negative disables", -5 * time.Minute, 0, false},
		{"short interval used as-is", 90 * time.Second, 90 * time.Second, true},
		{"at the 2m cap", 2 * time.Minute, 2 * time.Minute, true},
		{"long interval capped at 2m", 30 * time.Minute, 2 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cadence, enabled := refreshCadence(tc.interval)
			if enabled != tc.wantEnabled {
				t.Errorf("refreshCadence(%v) enabled = %v, want %v", tc.interval, enabled, tc.wantEnabled)
			}
			if cadence != tc.wantCadence {
				t.Errorf("refreshCadence(%v) cadence = %v, want %v", tc.interval, cadence, tc.wantCadence)
			}
		})
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
	if kept, _ := st.failRender(1, "engine: clone PR #1: gitclone: head ref no longer exists on remote"); kept {
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
	if kept, _ := st.failRender(1, "boom"); kept {
		t.Error("failRender kept a diff for a never-rendered PR")
	}
	if env, _ := st.get(1); env.Status != api.JobError || env.Error == "" {
		t.Fatalf("never-rendered failure: status=%q err=%q, want error", env.Status, env.Error)
	}

	// After a good render, a failure keeps the diff and flags refreshError
	// instead of clobbering it (a transient forge/git outage must not wipe it).
	st.setResult(1, api.DiffResult{PRNumber: 1})
	if kept, _ := st.failRender(1, "forge down"); !kept {
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

// TestStore_FailRenderReportsChanged verifies failRender's "changed" return — the
// failure-log de-spam signal. It must report a new message as changed, an
// identical repeat as unchanged (even across the setStatus(JobRunning) reset that
// runs before every render and clears errMsg), and reset after a successful render.
func TestStore_FailRenderReportsChanged(t *testing.T) {
	t.Parallel()
	st := newStore()
	st.upsertPR(api.PR{Number: 1, Open: true}, false)

	if _, changed := st.failRender(1, "boom"); !changed {
		t.Error("the first failure should report changed")
	}
	st.setStatus(api.PR{Number: 1}, api.JobRunning) // the per-render reset clears errMsg...
	if _, changed := st.failRender(1, "boom"); changed {
		t.Error("an identical repeat must report unchanged so its log is demoted")
	}
	if _, changed := st.failRender(1, "different"); !changed {
		t.Error("a changed message should report changed")
	}

	// A successful render clears the signature, so the next failure logs afresh.
	st.setResult(1, api.DiffResult{PRNumber: 1})
	if _, changed := st.failRender(1, "boom"); !changed {
		t.Error("after a successful render, the next failure should report changed")
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
			Warnings:  []api.Warning{{Level: api.LevelBlocking}, {Level: api.LevelCaution}, {Level: api.LevelCaution}},
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
	want := api.Signals{Resources: 2, Caution: 2, Blocking: 1, Images: 1, Failures: 1}
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

// TestServer_MonitoringHandler asserts the separate monitoring listener
// (MetricsAddr) serves /metrics together with the /healthz and /readyz probes,
// so chart probes can target it.
func TestServer_MonitoringHandler(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, okEngine())
	h := s.monitoringHandler()

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := do(h, "GET", path, nil, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
			t.Errorf("monitoring %s: %d %q", path, rec.Code, rec.Body.String())
		}
	}

	rec := do(h, "GET", "/metrics", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "konflate_") {
		t.Errorf("monitoring /metrics: %d, body lacks konflate_ metrics", rec.Code)
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

// TestRefreshList_RateLimitCircuitBreaker verifies the poll skips the forge while
// a rate-limit cooldown is in effect (a periodic tick or a webhook-driven relist
// during the window could only draw more 403s) and resumes once the reset has
// passed. A rate limit with no known reset time, or a generic non-rate-limit
// failure, must not wedge the poll.
func TestRefreshList_RateLimitCircuitBreaker(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		status   api.SyncStatus
		wantList int
	}{
		{"healthy → polls", api.SyncStatus{OK: true}, 1},
		{"rate-limited, resets in future → skipped", api.SyncStatus{OK: false, Reason: api.SyncRateLimited, RetryAt: now.Add(time.Hour).Unix()}, 0},
		{"rate-limited, reset passed → polls", api.SyncStatus{OK: false, Reason: api.SyncRateLimited, RetryAt: now.Add(-time.Minute).Unix()}, 1},
		{"rate-limited, no reset time → polls (never wedge)", api.SyncStatus{OK: false, Reason: api.SyncRateLimited}, 1},
		{"generic error → polls", api.SyncStatus{OK: false, Reason: api.SyncError}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{prs: []api.PR{{Number: 1, HeadSHA: "a"}}}
			s := newTestServer(t, ghCfg("tok"), prov, okEngine())
			s.sync.set(tc.status)

			s.refreshList(s.runCtx)

			if list, _ := prov.callCounts(); list != tc.wantList {
				t.Fatalf("ListPRs calls = %d, want %d", list, tc.wantList)
			}
		})
	}
}

// TestRefreshChecks_DisabledWhenAnonymous verifies the read-side CI-status poll is
// off on an anonymous instance (Config.ChecksEnabled) — the dominant per-poll forge
// cost, two calls per PR — and runs as normal once a token is configured.
func TestRefreshChecks_DisabledWhenAnonymous(t *testing.T) {
	prs := []api.PR{{Number: 1, HeadSHA: "a"}, {Number: 2, HeadSHA: "b"}}

	anon := &fakeProvider{prs: prs}
	newTestServer(t, ghCfg(""), anon, okEngine()).refreshList(t.Context())
	if _, checks := anon.callCounts(); checks != 0 {
		t.Errorf("anonymous: Checks calls = %d, want 0 (CI-status polling must be off)", checks)
	}

	authed := &fakeProvider{prs: prs}
	newTestServer(t, ghCfg("tok"), authed, okEngine()).refreshList(t.Context())
	if _, checks := authed.callCounts(); checks != len(prs) {
		t.Errorf("authenticated: Checks calls = %d, want %d (CI status polled per PR)", checks, len(prs))
	}
}

// TestMeta_FeaturesReflectAuth verifies /api/meta surfaces the checks feature gate
// so the UI hides what the backend won't feed: off when anonymous, on when authed.
func TestMeta_FeaturesReflectAuth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"anonymous", "", false},
		{"authenticated", "tok", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, ghCfg(tc.token), &fakeProvider{}, okEngine())
			var meta api.Meta
			mustJSON(t, do(s.mainHandler(), "GET", "/api/meta", nil, nil), &meta)
			if meta.Features.Checks != tc.want {
				t.Errorf("meta.Features.Checks = %v, want %v", meta.Features.Checks, tc.want)
			}
		})
	}
}

// TestRefreshPR_SkipsUnchangedRenderedHead verifies a metadata-only webhook
// (labeled/edited — same head SHA, already rendered) does not re-render, while a
// genuinely new head SHA does. Avoids a full render per chatty PR-metadata event.
func TestRefreshPR_SkipsUnchangedRenderedHead(t *testing.T) {
	t.Parallel()
	var renders atomic.Int32
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		renders.Add(1)
		return api.DiffResult{PRNumber: pr.Number, HeadSHA: pr.HeadSHA}, nil
	}}
	prov := &fakeProvider{}
	prov.setDetail(api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "main", HeadSHA: "abc"})
	s := newTestServer(t, ghCfg("tok"), prov, eng)

	// First webhook renders the head (no prior render → the gate lets it through).
	if err := s.refreshPR(s.runCtx, 1, "webhook", false); err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if env := waitFor(t, s, 1); env.Status != api.JobReady {
		t.Fatalf("first render status = %s, want ready", env.Status)
	}
	if n := renders.Load(); n != 1 {
		t.Fatalf("first refresh rendered %d times, want 1", n)
	}

	// A metadata-only webhook (same head SHA, already rendered) must not re-render.
	if err := s.refreshPR(s.runCtx, 1, "webhook", false); err != nil {
		t.Fatalf("refreshPR (metadata): %v", err)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := renders.Load(); n != 1 {
			t.Fatalf("a metadata-only refresh re-rendered (count=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A new head SHA re-renders.
	prov.setDetail(api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "main", HeadSHA: "def"})
	if err := s.refreshPR(s.runCtx, 1, "webhook", false); err != nil {
		t.Fatalf("refreshPR (new SHA): %v", err)
	}
	renderedDef := func() bool {
		env, ok := s.store.get(1)
		return ok && env.Status == api.JobReady && env.Diff != nil && env.Diff.HeadSHA == "def"
	}
	dl := time.Now().Add(2 * time.Second)
	for time.Now().Before(dl) && !renderedDef() {
		time.Sleep(5 * time.Millisecond)
	}
	if !renderedDef() {
		t.Fatal("a new head SHA never re-rendered to def")
	}
	if n := renders.Load(); n != 2 {
		t.Fatalf("total renders = %d, want 2 (initial + new-SHA; the metadata refresh skipped)", n)
	}

	// A retarget (same head SHA, new base ref → a different merge-base) re-renders.
	prov.setDetail(api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "develop", HeadSHA: "def"})
	if err := s.refreshPR(s.runCtx, 1, "webhook", false); err != nil {
		t.Fatalf("refreshPR (retarget): %v", err)
	}
	dl = time.Now().Add(2 * time.Second)
	for time.Now().Before(dl) && renders.Load() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := renders.Load(); n != 3 {
		t.Fatalf("a base-ref retarget must re-render; renders = %d, want 3", n)
	}

	// The push endpoint forces a re-render even on an unchanged, already-rendered
	// head+base (the CI trigger's whole point) — force bypasses the gate.
	if err := s.refreshPR(s.runCtx, 1, "push", true); err != nil {
		t.Fatalf("refreshPR (forced): %v", err)
	}
	dl = time.Now().Add(2 * time.Second)
	for time.Now().Before(dl) && renders.Load() < 4 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := renders.Load(); n != 4 {
		t.Fatalf("a forced refresh must re-render an unchanged head; renders = %d, want 4", n)
	}
}

// TestRefreshList_RerendersOnBaseRetarget verifies the poll/relist path re-renders
// a PR whose base was retargeted (same head SHA, new base → new merge-base), the
// only trigger in webhook-less polling mode besides the staleness backstop.
func TestRefreshList_RerendersOnBaseRetarget(t *testing.T) {
	t.Parallel()
	var renders atomic.Int32
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		renders.Add(1)
		return api.DiffResult{PRNumber: pr.Number, HeadSHA: pr.HeadSHA}, nil
	}}
	prov := &fakeProvider{}
	prov.setPRs(api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "main", HeadSHA: "abc"})
	s := newTestServer(t, ghCfg("tok"), prov, eng)

	s.refreshList(s.runCtx)
	if env := waitFor(t, s, 1); env.Status != api.JobReady {
		t.Fatalf("first render status = %s, want ready", env.Status)
	}
	if n := renders.Load(); n != 1 {
		t.Fatalf("first list render count = %d, want 1", n)
	}

	// An unchanged relist must not re-render.
	s.refreshList(s.runCtx)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := renders.Load(); n != 1 {
			t.Fatalf("an unchanged relist re-rendered (count=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A base retarget (same head, new base) must re-render.
	prov.setPRs(api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "develop", HeadSHA: "abc"})
	s.refreshList(s.runCtx)
	dl := time.Now().Add(2 * time.Second)
	for time.Now().Before(dl) && renders.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := renders.Load(); n != 2 {
		t.Fatalf("a base retarget via the list must re-render; renders = %d, want 2", n)
	}
}
