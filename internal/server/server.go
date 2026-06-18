// Package server is konflate's HTTP layer: a main server (UI, JSON API,
// websocket, optional inbound webhook/push) and a separate operational server
// for Prometheus metrics. Diff rendering is dispatched to a bounded,
// per-PR-coalescing job queue; results live in an in-memory store and stream to
// the UI over the websocket hub.
package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/persist"
	"github.com/home-operations/konflate/internal/provider"
)

// Engine renders a pull request into a diff. Declared here (rather than imported
// from internal/engine) so the server and its tests depend only on this
// behaviour — tests inject a fake and need none of flate's machinery.
type Engine interface {
	Diff(ctx context.Context, pr api.PR) (api.DiffResult, error)
}

// Server owns the HTTP servers and the diff pipeline.
type Server struct {
	cfg    *config.Config
	prov   provider.Provider
	writer provider.Writer // forge write-back (commit statuses); nil when disabled
	// checksRejected latches once the forge rejects a Check Run for lack of
	// permission (the App has no checks:write): subsequent renders skip straight to
	// the commit-status fallback instead of retrying a write that can't succeed.
	checksRejected atomic.Bool
	engine         Engine
	ui             fs.FS
	log            *slog.Logger

	// Version is the build version (main stamps it from ldflags after New);
	// served at /api/meta for the UI footer. "dev" or empty for local builds.
	Version string

	store   *store
	hub     *hub
	metrics *metrics
	sync    *syncTracker // forge read-polling health, for /api/meta + "sync" events
	queue   *queue
	runCtx  context.Context

	// prWrite serializes forge write-backs per PR (see writeBack): the queue can
	// finish a PR's in-flight and trailing renders back-to-back, each firing a
	// write-back, and two for the same PR must not race into duplicate comments.
	prWrite keyedMutex

	// relist is the coalescing signal for webhook-triggered re-lists. It holds a
	// single token, so a burst of inbound events collapses to one in-flight
	// refreshList plus at most one trailing run (see requestRelist/relistWorker)
	// rather than a goroutine + forge ListPRs per event. Buffered, created in New.
	relist chan struct{}

	// checkRefresh coalesces webhook-triggered check-rollup refreshes per head
	// SHA. One PR's CI cycle delivers a storm of check_run/check_suite events (a
	// Renovate batch, many heads at once); fanning out a goroutine + two forge
	// calls per event both hammered the forge — risking a secondary rate limit
	// that silently dropped the refresh, stranding a mid-CI snapshot until the
	// next list poll — and re-polled the same head dozens of times. checkWake
	// (single-token, created in New) nudges checkRefreshWorker, which drains
	// checkPending one SHA at a time: a burst for one head collapses to at most
	// one in-flight Checks poll plus one trailing poll, and distinct heads are
	// polled serially, never in a concurrent fan-out. checkPending is guarded by
	// checkMu.
	checkWake    chan struct{}
	checkMu      sync.Mutex
	checkPending map[string]struct{}

	avatarKey   []byte             // HMAC key for signing the same-origin /api/avatar proxy URLs
	mergeTmpl   *template.Template // renders the "copy to merge" command; nil disables it
	commentTmpl *template.Template // renders a custom PR-comment body; nil uses the default summary
}

// New assembles a Server. ui is the embedded UI filesystem (rooted at the
// directory holding index.html). The queue is created in Run, bound to the run
// context.
func New(cfg *config.Config, prov provider.Provider, eng Engine, ui fs.FS, log *slog.Logger) *Server {
	avatarKey := make([]byte, 32)
	_, _ = rand.Read(avatarKey) // crypto/rand; signs the same-origin avatar-proxy URLs

	// Optional write-back: a nil Writer (no credential) keeps konflate read-only.
	// A construction error (e.g. App-only auth on GitHub before it's wired)
	// disables write-back rather than failing startup.
	writer, werr := provider.NewWriter(cfg)
	if werr != nil {
		log.Warn("write-back disabled (no commit statuses will be posted)", "error", werr)
	}

	s := &Server{
		cfg: cfg, prov: prov, writer: writer, engine: eng, ui: ui, log: log,
		store: newStore(), hub: newHub(log), metrics: newMetrics(), sync: newSyncTracker(),
		avatarKey:    avatarKey,
		mergeTmpl:    newMergeTemplate(cfg, log),
		commentTmpl:  newCommentTemplate(cfg, log),
		relist:       make(chan struct{}, 1),
		checkWake:    make(chan struct{}, 1),
		checkPending: make(map[string]struct{}),
	}

	// Durability: persist rendered diffs under the (operator-persisted) cache
	// volume so the store — open PRs and the recently-merged shelf alike —
	// survives a restart, and reload them now. Best-effort: if the state dir
	// can't be created, log and run in-memory only (the prior behaviour).
	if p, err := persist.New(cfg.StateDir, log); err != nil {
		log.Warn("diff persistence disabled", "dir", cfg.StateDir, "error", err)
	} else {
		s.store.loadFrom(p, log)
		s.dropFilteredOnLoad()
	}

	return s
}

// Write-back tuning. forgeWriteTimeout is the overall budget for one write
// (across all attempts) so a slow or hung forge can't park a fire-and-forget
// goroutine until shutdown. A brief forge outage is retried a few times with
// exponential backoff rather than dropped — both writes are idempotent
// (SetStatus overwrites; UpsertComment edits its own comment), so a retry after a
// partial failure can't double-post. Beyond that the next render retries anyway.
const (
	forgeWriteTimeout  = 30 * time.Second // budget for one write-back, all attempts
	forgeWriteAttempts = 3                // tries before giving up
	forgeWriteBackoff  = time.Second      // base backoff, doubled each retry (1s, 2s, …)
	forgeVerifyTimeout = 10 * time.Second // startup credential check; don't block boot longer
)

// verifyWriteBack checks the write credential reaches the forge before any render
// posts. A permanent rejection (401/403/404 — bad token, missing permission, a
// wrong GitHub App installation, or an unreachable repo) disables write-back with
// a single error; a transient failure is logged and write-back left on to recover.
// Called once from Run before the queue starts, so flipping s.writer is race-free.
func (s *Server) verifyWriteBack(ctx context.Context) {
	vctx, cancel := context.WithTimeout(ctx, forgeVerifyTimeout)
	defer cancel()
	switch err := s.writer.Verify(vctx); {
	case err == nil:
		s.log.Info("write-back credential verified")
	case errors.Is(err, provider.ErrWriteAuthRejected):
		s.log.Error("write-back disabled: the forge rejected the write credential — "+
			"check the write token / GitHub App (client id, key, installation) and that it can write to this repo",
			"error", err)
		s.writer = nil
	default:
		s.log.Warn("could not verify the write-back credential; leaving it enabled to retry on renders", "error", err)
	}
}

// reportOutcome is the queue's terminal-outcome hook: it writes konflate's result
// back to the forge — a commit status and/or a summary comment, per the enabled
// toggles. Wired only when at least one is enabled and a Writer was built (see Run).
func (s *Server) reportOutcome(pr api.PR, st api.JobStatus, sig *api.Signals, errMsg string) {
	if s.writer == nil {
		return
	}
	if s.cfg.StatusChecksEnabled() {
		s.postStatus(pr, st, sig, errMsg)
	}
	if s.cfg.PRCommentsEnabled() {
		s.postComment(pr, st)
	}
}

// keyedMutex serializes work per integer key (a PR number): locking a key blocks
// only other holders of the same key, so distinct PRs proceed concurrently. The
// per-key mutex is reference-counted and dropped once idle, so the map can't grow
// without bound across a long-lived instance's PR numbers.
type keyedMutex struct {
	mu sync.Mutex
	m  map[int]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lock acquires key's mutex and returns its unlock func.
func (k *keyedMutex) lock(key int) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[int]*keyedLock)
	}
	kl := k.m[key]
	if kl == nil {
		kl = &keyedLock{}
		k.m[key] = kl
	}
	kl.refs++
	k.mu.Unlock()

	kl.mu.Lock()
	return func() {
		kl.mu.Unlock()
		k.mu.Lock()
		if kl.refs--; kl.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// writeBack runs one forge write off the render path: on its own goroutine,
// retried with backoff and bounded by forgeWriteTimeout, logging (never failing)
// on giving up so a write-back problem never blocks or fails a render. A write
// cut short by shutdown (runCtx cancelled) is draining, not a failure, so it
// isn't warned — matching the render queue's handling of context.Canceled.
//
// Write-backs for one PR are serialized (s.prWrite): the queue can finish two
// renders of a PR back-to-back (in-flight + trailing), each firing a write-back,
// and unserialized their UpsertComment find-then-create would race into duplicate
// comments. The later write instead waits, then sees and edits the earlier one's
// comment. The lock is taken before the timeout so the wait isn't charged against it.
func (s *Server) writeBack(kind string, number int, fn func(ctx context.Context) error) {
	go func() {
		unlock := s.prWrite.lock(number)
		defer unlock()
		ctx, cancel := context.WithTimeout(s.runCtx, forgeWriteTimeout)
		defer cancel()
		if err := retryWrite(ctx, forgeWriteAttempts, forgeWriteBackoff, func() error { return fn(ctx) }); err != nil &&
			!errors.Is(ctx.Err(), context.Canceled) {
			s.log.Warn("write-back failed", "kind", kind, "pr", number, "attempts", forgeWriteAttempts, "error", err)
		}
	}()
}

// retryWrite runs fn up to attempts times, returning as soon as it succeeds and
// backing off exponentially (base, 2·base, …) between tries. The backoff respects
// ctx — a cancelled or expired ctx stops further tries and returns fn's last
// error. It retries any error: both write-backs are idempotent, and classifying
// transient vs permanent across three forge SDKs would couple this to their error
// types for little gain (a permanent error just wastes a couple of bounded tries).
func retryWrite(ctx context.Context, attempts int, base time.Duration, fn func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil || attempt >= attempts {
			return err
		}
		if !sleepCtx(ctx, base<<(attempt-1)) {
			return err // ctx done during backoff — give up with the last error
		}
	}
}

// sleepCtx sleeps for d, or returns false early if ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// postStatus reports a terminal render outcome on the PR head. A GitHub App with
// checks:write gets a richer Check Run (a conclusion + a markdown report, gate-able
// as a required check); a write-PAT, GitLab/Forgejo, or an App without checks:write
// falls back to a plain commit status — so enabling write-back never regresses.
func (s *Server) postStatus(pr api.PR, st api.JobStatus, sig *api.Signals, errMsg string) {
	status := provider.Status{Context: s.cfg.StatusCheckName, TargetURL: s.reviewURL(pr.Number)}
	switch st {
	case api.JobReady:
		status.State = provider.StatusSuccess
		status.Description = renderedStatusDescription(sig)
	case api.JobError:
		status.State = provider.StatusFailure
		status.Description = truncateStatus("render failed: " + oneLine(errMsg))
	default:
		return
	}

	cr, canCheck := s.writer.(provider.CheckRunner)
	canCheck = canCheck && cr.ChecksSupported() && !s.checksRejected.Load()
	var check provider.CheckResult
	if canCheck {
		check = s.checkResult(pr, st, sig, errMsg)
	}

	s.writeBack("status", pr.Number, func(ctx context.Context) error {
		if canCheck {
			err := cr.CheckRun(ctx, pr, check)
			if !errors.Is(err, provider.ErrWriteAuthRejected) {
				return err // success, or a transient error worth retrying as a check run
			}
			// The App can't post check runs (no checks:write). Latch it so later
			// renders skip straight to the commit status, warning once rather than on
			// every render; fall back to the status for this one now.
			if s.checksRejected.CompareAndSwap(false, true) {
				s.log.Warn("check run rejected; falling back to a commit status (grant the GitHub App checks:write to post check runs)",
					"pr", pr.Number, "error", err)
			}
			canCheck = false
		}
		return s.writer.SetStatus(ctx, pr, status)
	})
}

// checkResult assembles the Check Run payload for a terminal outcome: a conclusion
// from the verdict and a markdown report reusing the comment summary renderer
// (GitHub renders its admonitions in check output too).
func (s *Server) checkResult(pr api.PR, st api.JobStatus, sig *api.Signals, errMsg string) provider.CheckResult {
	res := provider.CheckResult{
		Name:       s.cfg.StatusCheckName,
		DetailsURL: s.reviewURL(pr.Number),
		Conclusion: checkConclusion(st, sig),
	}
	if st == api.JobError {
		res.Title = truncateStatus("render failed: " + oneLine(errMsg))
	} else {
		res.Title = renderedStatusDescription(sig)
	}
	// The stored envelope drives the markdown body (it handles ready/error/pending);
	// fall back to the title alone if it's somehow gone.
	if env, ok := s.store.get(pr.Number); ok {
		res.Summary = summaryMarkdownBody(env, res.DetailsURL, true)
	} else {
		res.Summary = res.Title
	}
	return res
}

// checkConclusion maps a render verdict to a Check Run conclusion: a render error
// or any render failure fails the check; cautions are a non-blocking neutral; an
// otherwise-clean render passes.
func checkConclusion(st api.JobStatus, sig *api.Signals) string {
	switch {
	case st == api.JobError, sig != nil && sig.Failures > 0:
		return provider.CheckFailure
	case sig != nil && sig.Caution > 0:
		return provider.CheckNeutral
	default:
		return provider.CheckSuccess
	}
}

// postComment posts (or updates in place) the rendered summary as a PR comment.
// Only on a successful render: the comment exists to carry the summary, and a
// failed render is already surfaced by the commit status — so konflate doesn't
// create a "render failed" comment on a PR it could never render.
func (s *Server) postComment(pr api.PR, st api.JobStatus) {
	if st != api.JobReady {
		return
	}
	env, ok := s.store.get(pr.Number)
	if !ok || env.Diff == nil {
		return
	}
	body := s.commentBody(env)
	marker := konflateMarker(pr.Number)
	s.writeBack("comment", pr.Number, func(ctx context.Context) error {
		return s.writer.UpsertComment(ctx, pr, marker, body)
	})
}

// reviewURL is konflate's review link for a PR, built from KONFLATE_PUBLIC_URL;
// "" when that's unset (the status is then posted without a link).
func (s *Server) reviewURL(number int) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/#/pr/%d", base, number)
}

// renderedStatusDescription is the one-line summary on a successful status.
func renderedStatusDescription(sig *api.Signals) string {
	if sig == nil {
		return "Rendered the diff"
	}
	d := fmt.Sprintf("%d %s changed", sig.Resources, plural(sig.Resources, "resource", "resources"))
	if sig.Caution > 0 {
		d += fmt.Sprintf(", %d %s", sig.Caution, plural(sig.Caution, "caution", "cautions"))
	}
	if sig.Failures > 0 {
		d += fmt.Sprintf(", %d render %s", sig.Failures, plural(sig.Failures, "failure", "failures"))
	}
	return d
}

// oneLine flattens s to a single line for a commit-status description: every
// control character (newlines, tabs, CR, ESC, NUL, …) becomes a space and runs of
// whitespace collapse to one. A failed render's error message can echo
// fork-controlled manifest/template text, so this keeps it from injecting line
// breaks or terminal-control sequences into the status the forge displays.
func oneLine(s string) string {
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(mapped), " ")
}

// truncateStatus clamps a status description to the forge limit (GitHub caps the
// description at 140 characters). It counts runes, not bytes, so a multibyte
// character (an error message isn't guaranteed ASCII) is never split mid-rune.
func truncateStatus(s string) string {
	const max = 140
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// dropFilteredOnLoad re-applies the PR filter to everything restored from disk.
// The refresh loop re-checks open PRs against the filter, but the recently-merged
// shelf is never re-listed — so without this, a merged entry the current filter
// now excludes (e.g. a fork after tightening to !pr.fork) would linger across a
// restart, reclaimed only by retention. Drop those, which deletes their files.
//
// It removes a PR only on a clean exclusion (the filter genuinely says no). A
// filter *evaluation* error never deletes anything: a typo'd expression — which
// CEL accepts at compile time, since pr is a dynamic map — would otherwise wipe
// the whole recently-merged shelf on the next restart. Such a PR is kept as-is;
// checkFilter (in Run) fails startup on the same error, so the operator fixes
// the filter without losing persisted diffs.
func (s *Server) dropFilteredOnLoad() {
	dropped, errored := 0, 0
	for _, pr := range s.store.list() {
		switch allowed, err := s.prVerdict(pr.PR); {
		case err != nil:
			errored++ // keep it; never delete on an eval error (see above)
		case !allowed:
			s.store.remove(pr.Number)
			dropped++
		}
	}
	if dropped > 0 {
		s.log.Info("dropped restored PRs no longer matching the filter", "count", dropped)
	}
	if errored > 0 {
		s.log.Warn("kept restored PRs the filter could not evaluate; check KONFLATE_PR_FILTER_EXPR", "count", errored)
	}
}

// checkFilter smoke-tests the compiled PR filter against a fully-populated
// sample PR. The filter's pr variable is a dynamic map, so CEL accepts a
// reference to a field that doesn't exist (e.g. pr.isDraft for the real
// pr.draft) at compile time and only errors when evaluated — which at runtime
// would silently hide every PR. Evaluating one representative PR here turns that
// class of typo back into a fail-fast startup error, making good on the
// documented "fails fast at startup" guarantee. The sample carries every
// documented field (and one label, so label-field access is exercised — exists()
// over an empty list never runs its body). A nil filter (only in tests) is a
// no-op.
func (s *Server) checkFilter() error {
	if s.cfg.PRFilter == nil {
		return nil
	}
	sample := api.PR{
		Number: 1, Title: "sample", Author: "octocat", State: "open",
		Open: true, Merged: false, Draft: false, Fork: false,
		HeadRef: "feature", HeadSHA: "0000000", BaseRef: "main",
		URL:       "https://example.invalid/pr/1",
		CreatedAt: time.Unix(0, 0).UTC(),
		Labels:    []api.Label{{Name: "example", Color: "ededed"}},
	}
	if _, err := s.cfg.PRFilter.Eval(prFilterVars(sample)); err != nil {
		return err
	}
	return nil
}

// Run starts both servers and blocks until ctx is cancelled or a server fails,
// then drains in-flight diffs and shuts both down. It returns nil on a clean,
// signal-triggered shutdown.
func (s *Server) Run(ctx context.Context) error {
	// Fail fast on a filter that compiles but can't evaluate (e.g. a field typo,
	// which CEL accepts on the dynamic pr map) — it would otherwise hide every PR
	// at runtime. Before any goroutine starts or anything is served.
	if err := s.checkFilter(); err != nil {
		return fmt.Errorf("config: KONFLATE_PR_FILTER_EXPR: %w", err)
	}

	// gctx cancels when ctx is cancelled (shutdown) OR any server goroutine
	// returns an error. Bind the render queue, the refresh loops, and the
	// inbound-trigger goroutines (s.runCtx) to it so a server failure cancels
	// in-flight renders too — the drain below then returns promptly instead of
	// waiting out each render's full DiffTimeout while refreshLoop keeps feeding it.
	g, gctx := errgroup.WithContext(ctx)
	s.runCtx = gctx

	// Verify the write credential once, before any render can post: a permanent
	// rejection disables write-back with one clear log line instead of a per-render
	// warning storm; a transient failure leaves it enabled to recover on a render.
	// Runs before the queue/goroutines start, so flipping s.writer is race-free.
	wantWriteBack := s.cfg.StatusChecksEnabled() || s.cfg.PRCommentsEnabled()
	if s.writer != nil && wantWriteBack {
		s.verifyWriteBack(gctx)
	}

	// Wire write-back into the queue only when something is enabled and a Writer is
	// still configured; otherwise report stays nil and the queue skips it — konflate
	// posts nothing to the forge (the read-only default).
	var report reportFunc
	if s.writer != nil && wantWriteBack {
		report = s.reportOutcome
	}
	s.queue = newQueue(
		gctx, s.engine.Diff, s.store, s.hub.broadcast, s.reconcileHeadGone,
		s.metrics, s.log, s.cfg.MaxDiffConcurrency, report,
	)

	mainSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.Port),
		Handler:           s.mainHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the websocket connection is long-lived.
	}
	metricsSrv := &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           s.monitoringHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Warm the PR list at startup so the UI has content immediately, then run the
	// periodic refresh (re-list + per-PR staleness). Both are best effort —
	// failures are logged inside.
	go s.refreshList(gctx)
	go s.refreshLoop(gctx)
	go s.relistWorker(gctx)
	go s.checkRefreshWorker(gctx)

	g.Go(func() error { return serve(mainSrv, "main", s.log) })
	g.Go(func() error { return serve(metricsSrv, "metrics", s.log) })
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = mainSrv.Shutdown(sctx)
		_ = metricsSrv.Shutdown(sctx)
		s.queue.wait() // gctx is cancelled, so in-flight renders observe it and drain promptly
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func serve(srv *http.Server, name string, log *slog.Logger) error {
	log.Info("server listening", "server", name, "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

// requestRelist asks the relist worker to re-list open PRs, coalescing a burst
// of triggers into at most one in-flight run plus one trailing run. The signal
// channel holds a single token, so concurrent inbound webhooks collapse onto it
// instead of each spawning a goroutine + forge ListPRs call. Never blocks.
func (s *Server) requestRelist() {
	select {
	case s.relist <- struct{}{}:
	default: // a relist is already queued; fold this trigger into it
	}
}

// relistWorker serializes webhook-triggered relists: it runs refreshList at most
// once at a time, and a trigger that arrives mid-run schedules exactly one more
// run (the buffered signal). This caps the forge-API cost of a chatty
// "send everything" webhook — where one push's CI activity can fan out into
// dozens of relist-class deliveries — at one relist in flight plus one pending,
// instead of a full ListPRs per delivered event. Returns when ctx is cancelled.
func (s *Server) relistWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.relist:
			s.refreshList(ctx)
		}
	}
}

// requestCheckRefresh queues a check-rollup refresh for the open PR whose head is
// sha, coalescing per SHA (see the checkRefresh fields): repeated triggers for
// the same head — the storm of check_run/check_suite events one CI cycle delivers
// — fold into a single pending entry, and one arriving while that head's refresh
// is in flight schedules exactly one trailing poll. Never blocks the caller (the
// webhook handler): checkRefreshWorker does the forge I/O.
func (s *Server) requestCheckRefresh(sha string) {
	if sha == "" {
		return
	}
	s.checkMu.Lock()
	s.checkPending[sha] = struct{}{}
	s.checkMu.Unlock()
	select {
	case s.checkWake <- struct{}{}:
	default: // worker already nudged; it drains every pending SHA before sleeping
	}
}

// checkRefreshWorker serializes webhook-triggered check refreshes: on each wake it
// drains the pending-SHA set one head at a time through refreshChecksForSHA, so a
// burst of check events can't fan out into a swarm of concurrent forge calls. A
// trigger that arrives while a head's refresh runs re-adds it, yielding exactly
// one trailing poll — which lands after the storm settles, i.e. once the checks
// have actually completed, rather than capturing a mid-CI snapshot. Returns when
// ctx is cancelled.
func (s *Server) checkRefreshWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.checkWake:
		}
		for ctx.Err() == nil {
			s.checkMu.Lock()
			sha := ""
			for k := range s.checkPending {
				sha = k
				break
			}
			if sha == "" {
				s.checkMu.Unlock()
				break // drained; wait for the next wake
			}
			delete(s.checkPending, sha)
			s.checkMu.Unlock()
			s.refreshChecksForSHA(ctx, sha)
		}
	}
}

// refreshLoop is the background refresh. It wakes on a cadence and, once per
// RefreshInterval, re-lists PRs (discovering newly opened ones and reconciling
// closed ones); on every wake it re-renders any open PR whose render has gone
// stale. Staleness deadlines are jittered per PR (see staleJitter) so the open
// set — all rendered together at startup — doesn't re-render as one synchronized
// batch every interval. A PR a webhook just refreshed isn't re-rendered until it
// is genuinely stale. A RefreshInterval <=0 disables the loop entirely (inbound
// triggers only). Returns when ctx is cancelled.
func (s *Server) refreshLoop(ctx context.Context) {
	cadence, enabled := refreshCadence(s.cfg.RefreshInterval)
	if !enabled {
		// <=0 disables the periodic refresh: inbound webhooks/pushes are the only
		// triggers (the contract every sibling duration knob follows). Block until
		// shutdown so this errgroup goroutine still exits cleanly — without this we
		// would fall through and NewTicker(0) panics.
		s.log.Info("periodic refresh disabled (KONFLATE_REFRESH_INTERVAL<=0); relying on inbound triggers")
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(cadence)
	defer ticker.Stop()

	lastList := s.store.now() // startup already did the first list
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.store.now()
			if now.Sub(lastList) >= s.cfg.RefreshInterval {
				s.refreshList(ctx)
				lastList = now
			}
			s.refreshStale(now)
		}
	}
}

// refreshCadence resolves a refresh interval into the loop's ticker cadence and
// whether the periodic refresh runs at all. <=0 disables it (inbound triggers
// only). Otherwise the cadence is the interval capped at 2m, so the loop still
// wakes often enough to honor mid-interval per-PR staleness on a long interval;
// the interval itself is floored in config (minRefreshInterval), so this capped
// cadence can never hot-loop.
func refreshCadence(interval time.Duration) (cadence time.Duration, enabled bool) {
	if interval <= 0 {
		return 0, false
	}
	return min(interval, 2*time.Minute), true
}

// refreshStale re-renders every open PR whose last render is older than its
// staleness cadence — the backstop for an inbound webhook that never arrived. A
// PR whose head advanced uses RefreshInterval; one whose head is unchanged uses
// the slower RerenderInterval (it would otherwise re-render an identical diff).
func (s *Server) refreshStale(now time.Time) {
	stale := s.store.stalePRs(now, s.cfg.RefreshInterval, s.cfg.RerenderInterval)
	for _, pr := range stale {
		s.log.Debug("queuing render", "pr", pr.Number, "reason", "stale")
		s.queue.enqueue(pr)
	}
	// One summary line, not one per PR: the open set goes stale together, so the
	// per-PR detail would be a periodic burst of near-identical lines.
	if len(stale) > 0 {
		s.log.Info("queued stale renders", "count", len(stale))
	}
}

// monitoringHandler builds the mux for the separate monitoring server
// (MetricsAddr): /metrics plus the /healthz and /readyz probes. Consolidating
// the operational surface here keeps it off the main, possibly public-facing
// port; the probes also stay on the main mux for backward compatibility.
func (s *Server) monitoringHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", handleHealth)
	mux.Handle("GET /metrics", s.metrics.handler())
	return mux
}

// mainHandler builds the main mux. Inbound trigger endpoints (webhook, push)
// are served only when enabled; otherwise they explicitly return 501 so a
// misconfiguration is visible rather than a silent 404. The /healthz and
// /readyz probes are served here too (and on the monitoring port).
func (s *Server) mainHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", handleHealth)

	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/prs", s.handleListPRs)
	mux.HandleFunc("GET /api/prs/{number}/diff", s.handleDiff)
	mux.HandleFunc("GET /api/prs/{number}/summary", s.handleSummary)
	mux.HandleFunc("GET /api/avatar", s.handleAvatar)
	mux.HandleFunc("GET /ws", s.hub.serveWS)

	// No manual-refresh endpoint: konflate auto-refreshes (per-PR staleness +
	// re-list) so a public instance exposes no unauthenticated trigger. The
	// inbound webhook/push endpoints are served only when their own secret is
	// configured; otherwise they return 501 so a misconfiguration is visible.
	if s.cfg.PushEnabled() {
		mux.HandleFunc("POST /api/prs/{number}/refresh", s.handlePush)
	} else {
		mux.HandleFunc("POST /api/prs/{number}/refresh", handleDisabled)
	}
	if s.cfg.WebhookEnabled() {
		mux.HandleFunc("POST /hooks", s.handleWebhook)
	} else {
		mux.HandleFunc("POST /hooks", handleDisabled)
	}

	// Read-only MCP endpoint (opt-in): exposes the same rendered-diff analysis as
	// /api to an AI agent. The streamable transport uses POST (messages), GET (SSE),
	// and DELETE (session end); register each method explicitly so GET /mcp stays
	// more specific than the GET / UI catch-all (a bare "/mcp" would be ambiguous
	// against it and panic at registration).
	if s.cfg.MCPEnabled() {
		h := s.mcpHandler()
		mux.Handle("POST /mcp", h)
		mux.Handle("GET /mcp", h)
		mux.Handle("DELETE /mcp", h)
	}

	mux.Handle("GET /", s.uiHandler())

	return s.recoverer(s.accessLog(s.securityHeaders(mux)))
}

// securityHeaders applies a strict CSP and related headers to every response.
// script-src 'self' blocks injected inline scripts — the core XSS mitigation,
// since diff bodies carry server-rendered (chroma-escaped) HTML.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.metrics.httpReqs.WithLabelValues(statusClass(rec.status)).Inc()
		s.log.Debug("http",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for logging and metrics while
// staying transparent to handlers that need the underlying writer (the
// websocket upgrade hijacks the connection; Unwrap lets http.ResponseController
// reach through, and Hijack delegates directly as a fallback).
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter is not a Hijacker")
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}
