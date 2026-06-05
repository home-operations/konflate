package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/api"
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
	q := newQueue(context.Background(), diff, st, nil, newMetrics(), discardLog(), 2)
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
	q := newQueue(context.Background(), diff, st, nil, newMetrics(), discardLog(), 1)

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
	q := newQueue(context.Background(), diff, st, nil, newMetrics(), discardLog(), 1)

	st.upsertPR(api.PR{Number: 1})
	q.enqueue(api.PR{Number: 1})
	waitTerminal(t, st, 1)

	if env, _ := st.get(1); env.Status != api.JobError || !strings.Contains(env.Error, "panicked") {
		t.Fatalf("a render panic should mark the PR errored, got status=%q err=%q", env.Status, env.Error)
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
	q := newQueue(context.Background(), diff, st, nil, newMetrics(), discardLog(), 2)

	q.enqueue(api.PR{Number: 1})
	q.enqueue(api.PR{Number: 2})

	<-started // both renders must be running concurrently...
	<-started
	close(release) // ...before we release either

	waitTerminal(t, st, 1)
	waitTerminal(t, st, 2)
}
