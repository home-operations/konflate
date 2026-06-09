package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/gitclone"
)

// Websocket event types.
const (
	eventTypeStatus  = "status"  // a job changed state (pending/running/ready/error)
	eventTypeRemoved = "removed" // a PR is no longer open and was dropped from the store
)

// diffFunc renders a PR into a diff. It matches engine.Engine.Diff, so the real
// engine plugs in directly and tests pass a fake.
type diffFunc func(ctx context.Context, pr api.PR) (api.DiffResult, error)

// queue runs diff jobs with bounded concurrency and per-PR coalescing: while a
// PR is in flight, a second enqueue does not start a duplicate render — it
// records the PR as pending so exactly one more render happens after the
// current one finishes (so a webhook burst collapses to at most one trailing
// re-render). The pending entry holds the latest PR metadata, so the trailing
// render reflects the newest head SHA / title / labels rather than the snapshot
// that was in flight when the burst began.
type queue struct {
	diff      diffFunc
	store     *store
	notify    func(api.Event)
	reconcile func(number int) // called when a render finds the PR's head branch gone
	metrics   *metrics
	log       *slog.Logger

	ctx context.Context // cancelled on shutdown; threaded into every diff
	sem chan struct{}
	wg  sync.WaitGroup

	mu       sync.Mutex
	closing  bool // set by wait() during shutdown; a closing queue accepts no new work
	inflight map[int]struct{}
	pending  map[int]api.PR
}

func newQueue(
	ctx context.Context, diff diffFunc, st *store, notify func(api.Event),
	reconcile func(int), m *metrics, log *slog.Logger, concurrency int,
) *queue {
	if concurrency < 1 {
		concurrency = 1
	}
	return &queue{
		diff: diff, store: st, notify: notify, reconcile: reconcile, metrics: m, log: log,
		ctx:      ctx,
		sem:      make(chan struct{}, concurrency),
		inflight: map[int]struct{}{},
		pending:  map[int]api.PR{},
	}
}

// enqueue schedules a diff for pr, coalescing with any in-flight job for the
// same PR number.
func (q *queue) enqueue(pr api.PR) {
	// Which PRs reach the queue is decided upstream by the CEL PR filter
	// (config.PRFilterExpr) — forks are excluded by default and only tracked when
	// an operator's expression admits them. So whatever arrives here renders.
	q.mu.Lock()
	if q.closing {
		// Draining for shutdown: accept no new work. Gating wg.Add(1) here, under
		// the same lock wait() uses to set closing, is what keeps an Add from
		// racing wait()'s wg.Wait — a sync.WaitGroup misuse that can panic.
		q.mu.Unlock()
		return
	}
	if _, ok := q.inflight[pr.Number]; ok {
		q.pending[pr.Number] = pr // coalesce; remember the freshest metadata
		q.mu.Unlock()
		return
	}
	q.inflight[pr.Number] = struct{}{}
	q.wg.Add(1) // under mu and gated by !closing → never concurrent with wait()'s Wait
	q.setDepthLocked()
	q.mu.Unlock()

	q.store.setStatus(pr, api.JobPending)
	q.emit(pr.Number, api.JobPending, "")

	go q.run(pr)
}

// renderWithRecover runs the engine, converting a panic into an error for that
// one PR. A diff walks rendered cluster output that can hit edge cases; without
// this, a panic in any worker goroutine would crash the whole server (HTTP
// middleware recovers handlers, but these run off the request goroutine).
func (q *queue) renderWithRecover(pr api.PR) (res api.DiffResult, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("diff render panicked: %v", v)
			q.log.Error("diff render panicked", "pr", pr.Number, "panic", v, "stack", string(debug.Stack()))
		}
	}()
	return q.diff(q.ctx, pr)
}

func (q *queue) run(pr api.PR) {
	defer q.wg.Done()

	// Acquire a worker slot, or bail if we're shutting down before we got one.
	select {
	case q.sem <- struct{}{}:
	case <-q.ctx.Done():
		q.clear(pr.Number)
		return
	}
	defer func() { <-q.sem }()

	for {
		q.store.setStatus(pr, api.JobRunning)
		q.emit(pr.Number, api.JobRunning, "")
		q.log.Debug("rendering diff", "pr", pr.Number, "head", pr.HeadSHA)

		start := time.Now()
		res, err := q.renderWithRecover(pr)
		q.metrics.diffDuration.Observe(time.Since(start).Seconds())

		switch {
		case errors.Is(err, gitclone.ErrHeadRefGone):
			// The head branch vanished mid-flight: the PR was merged or closed and
			// its branch deleted between enqueue and render. Not a failure — keep any
			// diff already rendered and reconcile the PR (→ merged shelf / dropped).
			q.metrics.diffTotal.WithLabelValues("gone").Inc()
			q.log.Info("diff skipped: PR head ref gone (merged/closed mid-render)", "pr", pr.Number)
			if q.reconcile != nil {
				q.reconcile(pr.Number)
			}
		case errors.Is(err, context.Canceled):
			// q.ctx is cancelled only on shutdown, so a cancelled render means we're
			// draining — not a failure, and nothing to surface. Leave the stored diff
			// untouched (it re-renders next startup) and stop: don't run a coalesced
			// pending render whose context is just as dead.
			q.metrics.diffTotal.WithLabelValues("canceled").Inc()
			q.log.Debug("diff render canceled (shutting down)", "pr", pr.Number)
			q.clear(pr.Number)
			return
		case err != nil:
			// A real failure or a timeout (DiffTimeout/fetch deadline). Both are
			// transient as far as the store is concerned: failRender keeps the last
			// good render if there is one. Log a new failure at warn (kept) or error
			// (nothing to keep), but demote an identical repeat to debug — failRender
			// reports whether the message changed — so a chronically-broken PR or a
			// down forge doesn't re-warn on every refresh.
			outcome := "error"
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
			q.metrics.diffTotal.WithLabelValues(outcome).Inc()
			msg := err.Error()
			kept, changed := q.store.failRender(pr.Number, msg)
			detail, level := "diff render failed", slog.LevelError
			if kept {
				detail, level = "diff refresh failed; kept last render", slog.LevelWarn
			}
			if !changed {
				level = slog.LevelDebug // same failure as last time: don't re-warn
			}
			q.log.Log(context.Background(), level, detail, "pr", pr.Number, "outcome", outcome, "error", err)
			if kept {
				q.emit(pr.Number, api.JobReady, "")
			} else {
				q.emit(pr.Number, api.JobError, msg)
			}
		default:
			q.metrics.diffTotal.WithLabelValues("success").Inc()
			sig := q.store.setResult(pr.Number, res) // also clears the failure-dedup signature
			if sig == nil {
				sig = computeSignals(&res) // PR closed mid-render; nothing stored
			}
			q.log.Info("diff rendered", "pr", pr.Number, "duration", time.Since(start).Round(time.Millisecond),
				"resources", sig.Resources, "caution", sig.Caution, "images", sig.Images, "failures", sig.Failures)
			q.emit(pr.Number, api.JobReady, "")
		}

		q.mu.Lock()
		if next, ok := q.pending[pr.Number]; ok {
			delete(q.pending, pr.Number)
			q.mu.Unlock()
			pr = next // a refresh arrived mid-render; render the newest metadata
			continue
		}
		delete(q.inflight, pr.Number)
		q.setDepthLocked()
		q.mu.Unlock()
		return
	}
}

// clear drops a PR from the in-flight set (used when shutdown pre-empts it).
func (q *queue) clear(number int) {
	q.mu.Lock()
	delete(q.inflight, number)
	delete(q.pending, number)
	q.setDepthLocked()
	q.mu.Unlock()
}

// wait stops the queue accepting new work, then blocks until all in-flight jobs
// finish. Setting closing under mu before wg.Wait — paired with enqueue calling
// wg.Add only while holding mu and not closing — guarantees no Add runs
// concurrently with this Wait (a refresh tick or webhook goroutine landing
// mid-drain would otherwise risk Go's "WaitGroup misuse" panic, and any job it
// enqueued would leak past shutdown). Call after the context is cancelled so
// in-flight renders observe it and drain promptly.
func (q *queue) wait() {
	q.mu.Lock()
	q.closing = true
	q.mu.Unlock()
	q.wg.Wait()
}

func (q *queue) emit(number int, status api.JobStatus, errMsg string) {
	if q.notify != nil {
		q.notify(api.Event{Type: eventTypeStatus, Number: number, Status: status, Error: errMsg})
	}
}

func (q *queue) setDepthLocked() { q.metrics.queueDepth.Set(float64(len(q.inflight))) }
