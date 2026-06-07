// Package server is konflate's HTTP layer: a main server (UI, JSON API,
// websocket, optional inbound webhook/push) and a separate operational server
// for Prometheus metrics. Diff rendering is dispatched to a bounded,
// per-PR-coalescing job queue; results live in an in-memory store and stream to
// the UI over the websocket hub.
package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
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

	avatarKey []byte             // HMAC key for signing the same-origin /api/avatar proxy URLs
	mergeTmpl *template.Template // renders the "copy to merge" command; nil disables it
}

// New assembles a Server. ui is the embedded UI filesystem (rooted at the
// directory holding index.html). The queue is created in Run, bound to the run
// context.
func New(cfg *config.Config, prov provider.Provider, eng Engine, ui fs.FS, log *slog.Logger) *Server {
	avatarKey := make([]byte, 32)
	_, _ = rand.Read(avatarKey) // crypto/rand; signs the same-origin avatar-proxy URLs
	return &Server{
		cfg: cfg, prov: prov, engine: eng, ui: ui, log: log,
		store: newStore(), hub: newHub(log), metrics: newMetrics(),
		avatarKey: avatarKey,
		mergeTmpl: newMergeTemplate(cfg, log),
	}
}

// Run starts both servers and blocks until ctx is cancelled or a server fails,
// then drains in-flight diffs and shuts both down. It returns nil on a clean,
// signal-triggered shutdown.
func (s *Server) Run(ctx context.Context) error {
	s.runCtx = ctx
	s.queue = newQueue(ctx, s.engine.Diff, s.store, s.hub.broadcast, s.reconcileHeadGone, s.metrics, s.log, s.cfg.MaxDiffConcurrency)

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
	go s.refreshList(ctx)
	go s.refreshLoop(ctx)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serve(mainSrv, "main", s.log) })
	g.Go(func() error { return serve(metricsSrv, "metrics", s.log) })
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = mainSrv.Shutdown(sctx)
		_ = metricsSrv.Shutdown(sctx)
		s.queue.wait() // ctx is already cancelled; drain in-flight renders
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

// refreshLoop is the background refresh. It wakes on a cadence and, once per
// RefreshInterval, re-lists PRs (discovering newly opened ones and reconciling
// closed ones); on every wake it re-renders any open PR whose render has gone
// stale. Staleness deadlines are jittered per PR (see staleJitter) so the open
// set — all rendered together at startup — doesn't re-render as one synchronized
// batch every interval. A PR a webhook just refreshed isn't re-rendered until it
// is genuinely stale. Returns when ctx is cancelled.
func (s *Server) refreshLoop(ctx context.Context) {
	cadence := max(min(s.cfg.RefreshInterval, 2*time.Minute), time.Second)
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

// refreshStale re-renders every open PR whose last render is older than the
// refresh interval — the backstop for an inbound webhook that never arrived.
func (s *Server) refreshStale(now time.Time) {
	for _, pr := range s.store.stalePRs(now, s.cfg.RefreshInterval) {
		s.log.Info("queuing render", "pr", pr.Number, "reason", "stale")
		s.queue.enqueue(pr)
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

	return s.recoverer(s.accessLog(s.securityHeaders(s.gzipJSON(mux))))
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

// gzipPool reuses gzip writers across requests.
var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// gzipJSON compresses application/json responses — the rendered diff is large
// and highly compressible — when the client accepts gzip. It keys off the
// Content-Type the handler set, so only JSON is wrapped: the embedded UI (which
// does its own Range/conditional handling) and the hijacked websocket pass
// through untouched, as do clients that don't send Accept-Encoding: gzip.
func (s *Server) gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := &gzipResponseWriter{ResponseWriter: w}
		defer gz.close()
		next.ServeHTTP(gz, r)
	})
}

// gzipResponseWriter starts compressing only once it sees a JSON Content-Type
// (decided on the first WriteHeader/Write); until then it's a transparent
// pass-through, so a non-JSON response — or the websocket upgrade, which
// hijacks before writing — is never wrapped.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

func (g *gzipResponseWriter) decide() {
	if g.decided {
		return
	}
	g.decided = true
	if !strings.HasPrefix(g.Header().Get("Content-Type"), "application/json") {
		return
	}
	g.compress = true
	h := g.Header()
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length") // the compressed length differs; let it chunk
	g.gz = gzipPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	g.decide()
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	g.decide()
	if g.compress {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// close flushes and returns the gzip writer to the pool; a no-op when
// compression never started.
func (g *gzipResponseWriter) close() {
	if g.gz != nil {
		_ = g.gz.Close()
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}

func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// Hijack lets the websocket upgrade reach the underlying conn (it hijacks
// before any write, so compression never started).
func (g *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("gzipResponseWriter: underlying ResponseWriter is not a Hijacker")
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
