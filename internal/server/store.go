package server

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/home-operations/konflate/internal/api"
)

// job is the stored state of one pull request's diff computation.
type job struct {
	pr      api.PR
	status  api.JobStatus
	result  *api.DiffResult
	signals *api.Signals
	errMsg  string
	updated time.Time
	// closedAt is set once the PR has left the forge's open set (merged); zero
	// while open. A closed job keeps its last rendered diff frozen and is never
	// re-enqueued, and is pruned from the "recently merged" shelf by retention.
	closedAt time.Time
	// renderedAt is when this PR's diff last finished rendering (success or
	// error), zero until the first render. The refresh loop re-renders an open PR
	// once this is older than the refresh interval — the missed-webhook backstop.
	renderedAt time.Time
}

// store is the in-memory, concurrency-safe record of every known PR and the
// state of its diff. It is the single source of truth the HTTP handlers read
// from; the queue writes to it as jobs progress.
type store struct {
	mu   sync.RWMutex
	now  func() time.Time
	jobs map[int]*job
}

func newStore() *store {
	return &store{now: time.Now, jobs: map[int]*job{}}
}

// upsertPR records (or refreshes) a PR's metadata. A PR seen for the first time
// starts in the pending state; an already-tracked PR keeps its current status
// but takes the new metadata (title/labels/head may have changed).
func (s *store) upsertPR(pr api.PR) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[pr.Number]
	if j == nil {
		s.jobs[pr.Number] = &job{pr: pr, status: api.JobPending, updated: s.now()}
		return
	}
	j.pr = pr
	j.closedAt = time.Time{} // seen in the open set again (covers reopen)
	j.updated = s.now()
}

// setStatus transitions a PR to status, recording its metadata if new.
func (s *store) setStatus(pr api.PR, status api.JobStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[pr.Number]
	if j == nil {
		j = &job{pr: pr}
		s.jobs[pr.Number] = j
	}
	j.pr = pr
	j.status = status
	j.errMsg = ""
	j.updated = s.now()
}

// setResult stores a successful render and marks the PR ready.
func (s *store) setResult(number int, result api.DiffResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[number]; j != nil {
		r := result
		j.result = &r
		j.signals = computeSignals(&r)
		j.status = api.JobReady
		j.errMsg = ""
		j.updated = s.now()
		j.renderedAt = j.updated
	}
}

// computeSignals reduces a diff to its at-a-glance counts for the PR list.
func computeSignals(d *api.DiffResult) *api.Signals {
	s := &api.Signals{
		Resources: len(d.Resources),
		Images:    len(d.Images),
		Failures:  len(d.Failures),
	}
	for _, w := range d.Warnings {
		switch w.Level {
		case api.LevelDanger:
			s.Danger++
		case api.LevelCaution:
			s.Caution++
		}
	}
	return s
}

// setError marks the PR's diff as failed with msg.
func (s *store) setError(number int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[number]; j != nil {
		j.status = api.JobError
		j.errMsg = msg
		j.result = nil
		j.signals = nil
		j.updated = s.now()
		j.renderedAt = j.updated
	}
}

// get returns a snapshot of one PR's job, or false if unknown.
func (s *store) get(number int) (api.DiffEnvelope, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j := s.jobs[number]
	if j == nil {
		return api.DiffEnvelope{}, false
	}
	return api.DiffEnvelope{Status: j.status, PR: j.pr, Diff: j.result, Error: j.errMsg}, true
}

// activeNumbers returns the PRs currently treated as open (not yet marked
// closed). The full refresh uses this to find PRs that have left the forge's
// open set so it can classify them (merged → kept and frozen, abandoned →
// removed).
func (s *store) activeNumbers() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]int, 0, len(s.jobs))
	for n, j := range s.jobs {
		if j.closedAt.IsZero() {
			out = append(out, n)
		}
	}
	return out
}

// stalePRs returns the open PRs whose last render is at least interval old, so
// the refresh loop can re-render them (the missed-webhook backstop). PRs that
// have never rendered (in their initial render) and closed/merged PRs are
// excluded. The queue coalesces, so returning one that is mid-render is safe.
func (s *store) stalePRs(now time.Time, interval time.Duration) []api.PR {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []api.PR
	for _, j := range s.jobs {
		if j.closedAt.IsZero() && !j.renderedAt.IsZero() && now.Sub(j.renderedAt) >= interval {
			out = append(out, j.pr)
		}
	}
	return out
}

// markClosed freezes a PR as merged: it keeps its last rendered diff but stamps
// the close time so it moves to the "recently merged" shelf and stops being
// re-enqueued. The first close time wins (idempotent under concurrent
// refreshes); unknown PRs are ignored.
func (s *store) markClosed(number int, when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[number]
	if j == nil || !j.closedAt.IsZero() {
		return
	}
	j.closedAt = when
	j.pr.State = "merged"
	j.pr.Open = false
	j.pr.Merged = true
	j.updated = when
}

// remove drops a PR from the store entirely (used for abandoned PRs and by
// retention pruning). A render still in flight for it is harmless — its
// setResult/setStatus find no job and no-op.
func (s *store) remove(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, number)
}

// pruneClosed enforces the retention bounds on the merged shelf and returns the
// pruned PR numbers. A merged PR is dropped when it is older than maxAge OR
// beyond the maxCount most-recent (by close time) — whichever bites first. A
// non-positive bound disables that dimension. Open PRs are never touched.
func (s *store) pruneClosed(now time.Time, maxAge time.Duration, maxCount int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	closed := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !j.closedAt.IsZero() {
			closed = append(closed, j)
		}
	}
	slices.SortFunc(closed, func(a, b *job) int { return b.closedAt.Compare(a.closedAt) }) // newest first

	var removed []int
	for i, j := range closed {
		tooOld := maxAge > 0 && now.Sub(j.closedAt) > maxAge
		overCap := maxCount > 0 && i >= maxCount
		if tooOld || overCap {
			delete(s.jobs, j.pr.Number)
			removed = append(removed, j.pr.Number)
		}
	}
	return removed
}

// list returns every known PR with its job status, newest PR number first.
func (s *store) list() []api.PRStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]api.PRStatus, 0, len(s.jobs))
	for _, j := range s.jobs {
		var closedAt *time.Time
		if !j.closedAt.IsZero() {
			c := j.closedAt
			closedAt = &c
		}
		out = append(out, api.PRStatus{PR: j.pr, Status: j.status, Error: j.errMsg, UpdatedAt: j.updated, ClosedAt: closedAt, Signals: j.signals})
	}
	slices.SortFunc(out, func(a, b api.PRStatus) int { return cmp.Compare(b.Number, a.Number) }) // newest first
	return out
}
