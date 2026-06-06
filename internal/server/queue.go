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
	q.mu.Lock()
	if _, ok := q.inflight[pr.Number]; ok {
		q.pending[pr.Number] = pr // coalesce; remember the freshest metadata
		q.mu.Unlock()
		return
	}
	q.inflight[pr.Number] = struct{}{}
	q.setDepthLocked()
	q.mu.Unlock()

	q.store.setStatus(pr, api.JobPending)
	q.emit(pr.Number, api.JobPending, "")

	q.wg.Add(1)
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

		if errors.Is(err, gitclone.ErrHeadRefGone) {
			// The head branch vanished mid-flight: the PR was merged or closed and
			// its branch deleted between enqueue and render. Not a failure — keep any
			// diff already rendered and reconcile the PR (→ merged shelf / dropped).
			q.metrics.diffTotal.WithLabelValues("gone").Inc()
			q.log.Info("diff skipped: PR head ref gone (merged/closed mid-render)", "pr", pr.Number)
			if q.reconcile != nil {
				q.reconcile(pr.Number)
			}
		} else if err != nil {
			q.metrics.diffTotal.WithLabelValues("error").Inc()
			if q.store.failRender(pr.Number, err.Error()) {
				// A previously rendered diff is kept; flag the failed refresh
				// instead of clobbering it (transient forge/git outage, flaky pull).
				q.emit(pr.Number, api.JobReady, "")
				q.log.Warn("diff refresh failed; kept last render", "pr", pr.Number, "error", err)
			} else {
				q.emit(pr.Number, api.JobError, err.Error())
				q.log.Error("diff render failed", "pr", pr.Number, "error", err)
			}
		} else {
			q.metrics.diffTotal.WithLabelValues("success").Inc()
			sig := q.store.setResult(pr.Number, res)
			if sig == nil {
				sig = computeSignals(&res) // PR closed mid-render; nothing stored
			}
			q.log.Info("diff rendered", "pr", pr.Number, "duration", time.Since(start).Round(time.Millisecond),
				"resources", sig.Resources, "danger", sig.Danger, "images", sig.Images, "failures", sig.Failures)
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

// wait blocks until all in-flight jobs finish (call after cancelling ctx).
func (q *queue) wait() { q.wg.Wait() }

func (q *queue) emit(number int, status api.JobStatus, errMsg string) {
	if q.notify != nil {
		q.notify(api.Event{Type: eventTypeStatus, Number: number, Status: status, Error: errMsg})
	}
}

func (q *queue) setDepthLocked() { q.metrics.queueDepth.Set(float64(len(q.inflight))) }
