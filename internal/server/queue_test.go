package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/gitclone"
)

func waitTerminal(t *testing.T, st *store, number int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := st.get(number); ok && (env.Status == api.JobReady || env.Status == api.JobError) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("PR %d never reached a terminal status", number)
}

// TestQueue_CoalescesRerun verifies that enqueuing the same PR while it is
// rendering does not start a duplicate render, but does schedule exactly one
// trailing re-render — regardless of how many times it was enqueued.
func TestQueue_CoalescesRerun(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	started := make(chan struct{}, 10)
	release := make(chan struct{})

	diff := func(_ context.Context, pr api.PR) (api.DiffResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release // block until the test lets this render finish
		return api.DiffResult{PRNumber: pr.Number}, nil
	}

	st := newStore()
	q := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 2, true)
	pr := api.PR{Number: 1}

	q.enqueue(pr)
	<-started // first render is running

	q.enqueue(pr) // coalesced -> one pending re-render
	q.enqueue(pr) // coalesced again -> still just one pending re-render

	release <- struct{}{} // finish render #1
	<-started             // the single trailing re-render begins
	release <- struct{}{} // finish render #2

	waitTerminal(t, st, 1)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("diff called %d times, want exactly 2 (one render + one coalesced re-render)", got)
	}
}

// TestQueue_ForkGate verifies fork (cross-repo) PRs are not rendered unless
// rendering is opted in: with the gate off they are recorded as blocked and the
// engine is never called; same-repo PRs are unaffected; with the gate on a fork
// PR renders like any other.
func TestQueue_ForkGate(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	diff := func(_ context.Context, pr api.PR) (api.DiffResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return api.DiffResult{PRNumber: pr.Number}, nil
	}

	st := newStore()

	// Gate off: a fork PR is blocked synchronously and never rendered.
	off := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 1, false)
	off.enqueue(api.PR{Number: 1, Open: true, Fork: true})
	if env, ok := st.get(1); !ok || env.Status != api.JobBlocked {
		t.Fatalf("fork PR with gate off: status=%q ok=%v, want %q", env.Status, ok, api.JobBlocked)
	}
	mu.Lock()
	zero := calls
	mu.Unlock()
	if zero != 0 {
		t.Fatalf("fork PR was rendered despite the gate (%d calls)", zero)
	}

	// A same-repo PR is unaffected by the gate.
	off.enqueue(api.PR{Number: 2, Open: true})
	waitTerminal(t, st, 2)

	// Gate on: the fork PR renders.
	on := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 1, true)
	on.enqueue(api.PR{Number: 3, Open: true, Fork: true})
	waitTerminal(t, st, 3)

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("rendered %d PRs, want 2 (same-repo + opted-in fork)", calls)
	}
}

// TestQueue_CoalesceUsesLatestMetadata verifies that when a refresh is coalesced
// into an in-flight render, the trailing re-render uses the newest PR metadata
// (e.g. a head SHA that advanced mid-render), not the snapshot that was running.
func TestQueue_CoalesceUsesLatestMetadata(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string // head SHA each render observed, in order
	started := make(chan struct{}, 10)
	release := make(chan struct{})

	diff := func(_ context.Context, pr api.PR) (api.DiffResult, error) {
		mu.Lock()
		seen = append(seen, pr.HeadSHA)
		mu.Unlock()
		started <- struct{}{}
		<-release
		return api.DiffResult{PRNumber: pr.Number}, nil
	}

	st := newStore()
	q := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 1, true)

	q.enqueue(api.PR{Number: 1, HeadSHA: "old"})
	<-started // "old" is rendering

	// A new push lands mid-render: enqueue the newer metadata, which coalesces.
	st.upsertPR(api.PR{Number: 1, HeadSHA: "new"})
	q.enqueue(api.PR{Number: 1, HeadSHA: "new"})

	release <- struct{}{} // finish "old"
	<-started             // trailing re-render begins
	release <- struct{}{} // finish it
	waitTerminal(t, st, 1)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "old" || seen[1] != "new" {
		t.Fatalf("renders saw %v, want [old new]", seen)
	}
	if env, _ := st.get(1); env.PR.HeadSHA != "new" {
		t.Fatalf("store head SHA = %q, want %q", env.PR.HeadSHA, "new")
	}
}

// TestQueue_RecoversRenderPanic verifies a panic in the engine marks that PR
// errored instead of crashing the process (the worker runs in its own
// goroutine, so an unrecovered panic would take the whole server down).
func TestQueue_RecoversRenderPanic(t *testing.T) {
	t.Parallel()
	diff := func(context.Context, api.PR) (api.DiffResult, error) { panic("boom") }
	st := newStore()
	q := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 1, true)

	st.upsertPR(api.PR{Number: 1})
	q.enqueue(api.PR{Number: 1})
	waitTerminal(t, st, 1)

	if env, _ := st.get(1); env.Status != api.JobError || !strings.Contains(env.Error, "panicked") {
		t.Fatalf("a render panic should mark the PR errored, got status=%q err=%q", env.Status, env.Error)
	}
}

// TestQueue_HeadRefGoneReconcilesNotErrors verifies that when a render fails
// because the PR's head branch is gone (merged/closed mid-flight), the queue
// reconciles the PR instead of marking it errored — and a diff rendered earlier
// (while it was open) is left intact rather than clobbered.
func TestQueue_HeadRefGoneReconcilesNotErrors(t *testing.T) {
	t.Parallel()
	diff := func(context.Context, api.PR) (api.DiffResult, error) {
		return api.DiffResult{}, fmt.Errorf("engine: clone PR #1: %w", gitclone.ErrHeadRefGone)
	}
	reconciled := make(chan int, 1)
	st := newStore()
	st.upsertPR(api.PR{Number: 1})
	st.setResult(1, api.DiffResult{PRNumber: 1}) // a diff from when the PR was open
	q := newQueue(context.Background(), diff, st, nil, func(n int) { reconciled <- n }, newMetrics(), discardLog(), 1, true)

	q.enqueue(api.PR{Number: 1})

	select {
	case n := <-reconciled:
		if n != 1 {
			t.Fatalf("reconcile called with PR %d, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a head-gone render never triggered reconcile")
	}

	if env, _ := st.get(1); env.Status == api.JobError {
		t.Fatalf("head-gone render must not mark the PR errored, got status=%q err=%q", env.Status, env.Error)
	} else if env.Diff == nil {
		t.Fatal("head-gone render clobbered the previously rendered diff")
	}
}

// TestQueue_RunsConcurrently verifies that distinct PRs render in parallel up to
// the concurrency limit: with limit 2, both must start before either finishes
// (otherwise the second <-started would deadlock the test).
func TestQueue_RunsConcurrently(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 10)
	release := make(chan struct{})

	diff := func(_ context.Context, pr api.PR) (api.DiffResult, error) {
		started <- struct{}{}
		<-release
		return api.DiffResult{PRNumber: pr.Number}, nil
	}

	st := newStore()
	q := newQueue(context.Background(), diff, st, nil, nil, newMetrics(), discardLog(), 2, true)

	q.enqueue(api.PR{Number: 1})
	q.enqueue(api.PR{Number: 2})

	<-started // both renders must be running concurrently...
	<-started
	close(release) // ...before we release either

	waitTerminal(t, st, 1)
	waitTerminal(t, st, 2)
}
