package server

import (
	"cmp"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/persist"
)

// job is the stored state of one pull request's diff computation.
type job struct {
	pr api.PR
	// hidden marks a PR the CEL filter excludes: it is still tracked and listed
	// (greyed, under the "hidden" pill) but never enqueued for rendering, so a
	// fork's untrusted code never runs. Re-evaluated on every upsert.
	hidden  bool
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
	// refreshErr holds the message from the most recent failed re-render while an
	// earlier diff is still shown; empty after a success. Lets the UI flag
	// "couldn't refresh" without discarding the last-good diff.
	refreshErr string
	// lastFailMsg is the most recent render-failure message, kept solely to
	// de-duplicate failure logs: the queue logs a new failure once, then demotes
	// identical repeats to debug so a chronically-broken PR doesn't re-warn every
	// refresh. Unlike errMsg it survives the setStatus(JobRunning) reset, so the
	// dedup spans render attempts; it is cleared on a successful render and dies
	// with the job, so unlike a queue-side map it can't leak across the PR's life.
	lastFailMsg string
	// savedDigest is the content hash (timestamps excluded) of what's currently
	// on disk for this PR. The staleness backstop re-renders open PRs ~every
	// interval even when nothing changed; comparing against this skips rewriting
	// an identical multi-MB diff each time.
	savedDigest uint64
}

// store is the in-memory, concurrency-safe record of every known PR and the
// state of its diff. It is the single source of truth the HTTP handlers read
// from; the queue writes to it as jobs progress.
type store struct {
	mu   sync.RWMutex
	now  func() time.Time
	jobs map[int]*job
	// persist (nil = durability disabled) writes each rendered/merged job to disk
	// and is reloaded at startup; log records its rare I/O failures. Both are set
	// once by loadFrom before the server starts serving, then only read, so the
	// save/del helpers may touch them after releasing s.mu.
	persist *persist.Store
	log     *slog.Logger
}

func newStore() *store {
	return &store{now: time.Now, jobs: map[int]*job{}}
}

// loadFrom attaches a persistence store and restores the records it holds into
// this (freshly built, empty) store. Records are restored verbatim; the next
// refresh reconciles them against the forge — an open PR re-renders only if its
// head advanced, a merged PR is dropped only once past retention. Signals are
// recomputed from the diff rather than persisted. Call once, at startup.
func (s *store) loadFrom(p *persist.Store, log *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = p
	s.log = log
	for _, rec := range p.Load() {
		j := &job{
			pr:         rec.PR,
			status:     rec.Status,
			result:     rec.Result,
			errMsg:     rec.ErrMsg,
			refreshErr: rec.RefreshErr,
			updated:    rec.Updated,
			closedAt:   rec.ClosedAt,
			renderedAt: rec.RenderedAt,
		}
		if rec.Result != nil {
			j.signals = computeSignals(rec.Result)
		}
		j.savedDigest = contentDigest(rec) // so an identical re-render after restart skips re-writing
		s.jobs[rec.PR.Number] = j
	}
}

// record snapshots a job into its durable form. Held under the store lock.
func (j *job) record() persist.Record {
	return persist.Record{
		PR:         j.pr,
		Status:     j.status,
		Result:     j.result,
		ErrMsg:     j.errMsg,
		RefreshErr: j.refreshErr,
		Updated:    j.updated,
		ClosedAt:   j.closedAt,
		RenderedAt: j.renderedAt,
	}
}

// contentDigest hashes the parts of a record that define its persisted content,
// excluding the volatile Updated/RenderedAt timestamps. Two renders of the same
// PR that produce the same diff (the common staleness-backstop case) hash equal,
// so the store can skip rewriting the file; a real change (diff moved, merged,
// metadata edited) hashes differently and is persisted.
func contentDigest(rec persist.Record) uint64 {
	rec.Updated, rec.RenderedAt = time.Time{}, time.Time{}
	b, _ := json.Marshal(rec) // Record is always marshalable
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// save / del run the persistence I/O. Callers snapshot under s.mu, release it,
// then call these — the file write/remove never holds the store lock. No-ops
// when durability is disabled.
func (s *store) save(rec persist.Record) error {
	if s.persist == nil {
		return nil
	}
	if err := s.persist.Save(rec); err != nil {
		if s.log != nil {
			s.log.Warn("persist render", "pr", rec.PR.Number, "error", err)
		}
		return err
	}
	return nil
}

func (s *store) del(number int) {
	if s.persist == nil {
		return
	}
	if err := s.persist.Delete(number); err != nil && s.log != nil {
		s.log.Warn("persist delete", "pr", number, "error", err)
	}
}

// upsertPR records (or refreshes) a PR's metadata. A PR seen for the first time
// starts in the pending state; an already-tracked PR keeps its current status
// but takes the new metadata (title/labels/head may have changed).
func (s *store) upsertPR(pr api.PR, hidden bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[pr.Number]
	if j == nil {
		s.jobs[pr.Number] = &job{pr: pr, status: api.JobPending, updated: s.now(), hidden: hidden}
		return
	}
	j.pr = pr
	j.hidden = hidden
	j.closedAt = time.Time{} // seen in the open set again (covers reopen)
	j.updated = s.now()
}

// setStatus transitions a PR to status, recording its metadata if new. A PR
// already frozen on the merged shelf is left untouched, so a render that was
// enqueued before the PR merged cannot drag it back into a live state.
func (s *store) setStatus(pr api.PR, status api.JobStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[pr.Number]
	if j != nil && !j.closedAt.IsZero() {
		return
	}
	if j == nil {
		j = &job{pr: pr}
		s.jobs[pr.Number] = j
	}
	j.pr = pr
	j.status = status
	j.errMsg = ""
	j.updated = s.now()
}

// setResult stores a successful render and marks the PR ready, returning the
// signals it computed (nil if the PR was concurrently closed and nothing was
// stored) so the caller can log them without recomputing.
func (s *store) setResult(number int, result api.DiffResult) *api.Signals {
	s.mu.Lock()
	j := s.jobs[number]
	if j == nil || !j.closedAt.IsZero() {
		s.mu.Unlock()
		return nil
	}
	r := result
	j.result = &r
	j.signals = computeSignals(&r)
	j.status = api.JobReady
	j.errMsg = ""
	j.refreshErr = ""
	j.lastFailMsg = "" // recovered: the next failure logs afresh
	j.updated = s.now()
	j.renderedAt = j.updated
	sig := j.signals
	rec := j.record()
	prev := j.savedDigest
	s.mu.Unlock()

	// Hash and persist outside the lock: contentDigest marshals the whole record
	// (multi-MB of rendered HTML), and holding s.mu across that stalls every
	// reader (the list/diff/summary handlers) and every mutator. The snapshot is
	// safe to read unlocked — j.result is replaced wholesale, never mutated in
	// place, and per-PR coalescing means no second setResult for this PR runs
	// concurrently.
	s.persistRecord(number, rec, prev)
	return sig
}

// persistRecord computes rec's content digest and, when it differs from prev,
// writes rec and commits the new digest as savedDigest — but only after the save
// succeeds. So a failed persist (a full or read-only volume) leaves savedDigest
// unchanged, and a later identical re-render retries the write rather than
// skipping it as "already on disk" (which would strand stale content that a
// restart then restores). The costly marshal runs off the store lock; the digest
// is committed under a brief re-lock.
func (s *store) persistRecord(number int, rec persist.Record, prev uint64) {
	d := contentDigest(rec)
	if d == prev {
		return // unchanged: nothing to write, and savedDigest is already current
	}
	if err := s.save(rec); err != nil {
		return // leave savedDigest as-is so the next render retries the write
	}
	s.mu.Lock()
	if j := s.jobs[number]; j != nil {
		j.savedDigest = d
	}
	s.mu.Unlock()
}

// computeSignals reduces a diff to its at-a-glance counts for the PR list.
func computeSignals(d *api.DiffResult) *api.Signals {
	return &api.Signals{
		Resources: len(d.Resources),
		Caution:   len(d.Warnings), // every warning is a caution (the sole severity)
		Images:    len(d.Images),
		Failures:  len(d.Failures),
	}
}

// failRender records a render failure. If the PR still has a previously rendered
// diff, that diff is kept and the failure is flagged via refreshErr instead — so
// a transient forge/git outage or a flaky source pull doesn't wipe a good render;
// the UI keeps showing it with a "couldn't refresh" marker. Only a PR that never
// rendered flips to the error state. It returns whether a prior diff was kept,
// and whether this failure message differs from the previous one — letting the
// caller log a new failure prominently but demote identical repeats. A PR frozen
// on the merged shelf is left untouched (see setStatus).
func (s *store) failRender(number int, msg string) (kept, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[number]
	if j == nil || !j.closedAt.IsZero() {
		return false, false
	}
	changed = j.lastFailMsg != msg
	j.lastFailMsg = msg
	j.updated = s.now()
	j.renderedAt = j.updated
	if j.result != nil {
		j.refreshErr = msg // keep result/signals and the existing status
		return true, changed
	}
	j.status = api.JobError
	j.errMsg = msg
	return false, changed
}

// get returns a snapshot of one PR's job, or false if unknown.
func (s *store) get(number int) (api.DiffEnvelope, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j := s.jobs[number]
	if j == nil {
		return api.DiffEnvelope{}, false
	}
	return api.DiffEnvelope{
		Status:       j.status,
		PR:           j.pr,
		Diff:         j.result,
		Error:        j.errMsg,
		RefreshError: j.refreshErr,
		Hidden:       j.hidden,
		Digest:       j.savedDigest,
	}, true
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

// stalePRs returns the open PRs whose last render is older than their jittered
// staleness deadline, so the refresh loop can re-render them (the missed-webhook
// backstop). PRs still in their initial render and closed/merged PRs are
// excluded. The queue coalesces, so returning one that is mid-render is safe.
//
// The deadline is interval ± a deterministic per-PR jitter (see staleJitter):
// without it, the whole open set — all rendered together at startup — would go
// stale on the same tick and re-render as one synchronized batch every interval
// (a thundering herd on the forge and CPU). Jitter spreads them across the
// window while keeping the average period ≈ interval.
func (s *store) stalePRs(now time.Time, interval time.Duration) []api.PR {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []api.PR
	for _, j := range s.jobs {
		if !j.hidden && j.closedAt.IsZero() && !j.renderedAt.IsZero() && now.Sub(j.renderedAt) >= interval+staleJitter(j.pr.Number, interval) {
			out = append(out, j.pr)
		}
	}
	return out
}

// staleJitter returns a deterministic per-PR offset in [-interval/4, +interval/4)
// added to the staleness deadline. Deterministic (a 64-bit Fibonacci hash of the
// PR number) so a PR's cadence is stable rather than oscillating, and symmetric
// so the average refresh period stays ≈ interval while PRs rendered together are
// pulled apart in time.
func staleJitter(prNumber int, interval time.Duration) time.Duration {
	spread := int64(interval / 2)
	if spread <= 0 {
		return 0
	}
	h := uint64(prNumber) * 11400714819323198485 // 2^64 / golden ratio
	return time.Duration(int64(h%uint64(spread)) - spread/2)
}

// markClosed freezes a PR as merged: it keeps its last rendered diff but stamps
// the close time so it moves to the "recently merged" shelf and stops being
// re-enqueued. The first close time wins (idempotent under concurrent
// refreshes); unknown PRs are ignored.
func (s *store) markClosed(number int, when time.Time) {
	s.mu.Lock()
	j := s.jobs[number]
	if j == nil || !j.closedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	j.closedAt = when
	j.pr.State = "merged"
	j.pr.Open = false
	j.pr.Merged = true
	j.updated = when
	rec := j.record()
	prev := j.savedDigest
	s.mu.Unlock()

	// Re-record with the merge stamp so the shelf survives a restart — off the
	// lock; the merge always changes the content, so this writes.
	s.persistRecord(number, rec, prev)
}

// remove drops a PR from the store entirely (used for abandoned PRs and by
// retention pruning). A render still in flight for it is harmless — its
// setResult/setStatus find no job and no-op.
func (s *store) remove(number int) {
	s.mu.Lock()
	delete(s.jobs, number)
	s.mu.Unlock()

	s.del(number)
}

// pruneClosed enforces the retention bounds on the merged shelf and returns the
// pruned PR numbers. A merged PR is dropped when it is older than maxAge OR
// beyond the maxCount most-recent (by close time) — whichever bites first. A
// non-positive bound disables that dimension. Open PRs are never touched.
func (s *store) pruneClosed(now time.Time, maxAge time.Duration, maxCount int) []int {
	s.mu.Lock()
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
	s.mu.Unlock()

	for _, n := range removed {
		s.del(n) // evict the pruned PRs from disk too
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
		out = append(out, api.PRStatus{
			PR: j.pr, Status: j.status, Error: j.errMsg, RefreshError: j.refreshErr,
			UpdatedAt: j.updated, ClosedAt: closedAt, Signals: j.signals, Hidden: j.hidden,
		})
	}
	slices.SortFunc(out, func(a, b api.PRStatus) int { return cmp.Compare(b.Number, a.Number) }) // newest first
	return out
}
