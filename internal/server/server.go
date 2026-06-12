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
	"text/template"
	"time"

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
	engine Engine
	ui     fs.FS
	log    *slog.Logger

	// Version is the build version (main stamps it from ldflags after New);
	// served at /api/meta for the UI footer. "dev" or empty for local builds.
	Version string

	store   *store
	hub     *hub
	metrics *metrics
	queue   *queue
	runCtx  context.Context

	// relist is the coalescing signal for webhook-triggered re-lists. It holds a
	// single token, so a burst of inbound events collapses to one in-flight
	// refreshList plus at most one trailing run (see requestRelist/relistWorker)
	// rather than a goroutine + forge ListPRs per event. Buffered, created in New.
	relist chan struct{}

	avatarKey []byte             // HMAC key for signing the same-origin /api/avatar proxy URLs
	mergeTmpl *template.Template // renders the "copy to merge" command; nil disables it
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
		store: newStore(), hub: newHub(log), metrics: newMetrics(),
		avatarKey: avatarKey,
		mergeTmpl: newMergeTemplate(cfg, log),
		relist:    make(chan struct{}, 1),
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

// statusContext is the name konflate's commit status appears under on the PR.
const statusContext = "konflate"

// statusWriteTimeout bounds a single forge status write so a slow or hung forge
// can't park a write-back goroutine until shutdown (it's fire-and-forget off the
// render path). Generous: a status POST is a small request.
const statusWriteTimeout = 30 * time.Second

// reportStatus writes konflate's commit status for a terminal render outcome,
// when status write-back is enabled. It runs the forge write on a goroutine —
// it must never block the render queue — and logs (never fails) on error. Wired
// to the queue only when StatusChecksEnabled (see Run).
func (s *Server) reportStatus(pr api.PR, st api.JobStatus, sig *api.Signals, errMsg string) {
	if s.writer == nil {
		return
	}
	status := provider.Status{Context: statusContext, TargetURL: s.reviewURL(pr.Number)}
	switch st {
	case api.JobReady:
		status.State = provider.StatusSuccess
		status.Description = renderedStatusDescription(sig)
	case api.JobError:
		status.State = provider.StatusFailure
		status.Description = truncateStatus("render failed: " + errMsg)
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.runCtx, statusWriteTimeout)
		defer cancel()
		if err := s.writer.SetStatus(ctx, pr, status); err != nil {
			s.log.Warn("status write-back failed", "pr", pr.Number, "error", err)
		}
	}()
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

	// Wire status write-back into the queue only when it's enabled and a Writer
	// was built; otherwise report stays nil and the queue skips it — konflate
	// posts nothing to the forge (the read-only default).
	var report reportFunc
	if s.cfg.StatusChecksEnabled() && s.writer != nil {
		report = s.reportStatus
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
		Handler:           s.metrics.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Warm the PR list at startup so the UI has content immediately, then run the
	// periodic refresh (re-list + per-PR staleness). Both are best effort —
	// failures are logged inside.
	go s.refreshList(gctx)
	go s.refreshLoop(gctx)
	go s.relistWorker(gctx)

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

// refreshStale re-renders every open PR whose last render is older than the
// refresh interval — the backstop for an inbound webhook that never arrived.
func (s *Server) refreshStale(now time.Time) {
	stale := s.store.stalePRs(now, s.cfg.RefreshInterval)
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

// mainHandler builds the main mux. Inbound trigger endpoints (webhook, push)
// are served only when enabled; otherwise they explicitly return 501 so a
// misconfiguration is visible rather than a silent 404.
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
