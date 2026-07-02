package server

import (
	"os"
	"sync/atomic"
	"testing"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/gitclone"
	"github.com/home-operations/konflate/internal/persist"
)

// #2 — a render enqueued before its PR was dropped must not recreate it.
func TestStore_SetStatusDoesNotResurrectRemovedPR(t *testing.T) {
	t.Parallel()
	st := newStore()
	st.upsertPR(api.PR{Number: 1, Open: true}, false)
	st.remove(1) // abandoned/pruned while its render sat queued behind the semaphore

	st.setStatus(api.PR{Number: 1, Open: true}, api.JobRunning)
	if _, ok := st.get(1); ok {
		t.Fatal("setStatus resurrected a removed PR")
	}
	if sig := st.setResult(1, api.DiffResult{PRNumber: 1}); sig != nil {
		t.Fatal("setResult returned signals for a removed PR")
	}
	if _, ok := st.get(1); ok {
		t.Fatal("setResult resurrected a removed PR")
	}
}

// #3 — a stale point "open" snapshot must not un-shelve a merged PR; only the
// authoritative open-list path (markOpen) un-shelves a genuine reopen.
func TestStore_MergedShelfSurvivesStaleOpenSnapshot(t *testing.T) {
	t.Parallel()
	st := newStore()
	st.upsertPR(api.PR{Number: 1, Open: true}, false)
	st.markClosed(1, st.now())

	st.upsertPR(api.PR{Number: 1, Open: true}, false) // delayed refreshPR racing the merge
	if prStatus(st, 1).ClosedAt == nil {
		t.Fatal("upsertPR un-shelved a merged PR from a stale open snapshot")
	}

	if !st.markOpen(api.PR{Number: 1, Open: true}, false) {
		t.Fatal("markOpen should report the reopen of a shelved PR")
	}
	if prStatus(st, 1).ClosedAt != nil {
		t.Fatal("markOpen should have un-shelved the reopened PR")
	}
}

// #4 — the diff ETag digest must track the in-memory body even when the persist
// write fails, so a changed diff still revalidates (no stale 304).
func TestStore_ETagDigestUpdatesEvenWhenPersistFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, err := persist.New(dir, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	st := newStore()
	st.loadFrom(p, discardLog())
	st.upsertPR(api.PR{Number: 1, Open: true}, false)

	st.setResult(1, api.DiffResult{PRNumber: 1, HeadSHA: "a", Resources: []api.DiffResource{{ID: "r0"}}})
	env1, _ := st.get(1)

	// Make persistence fail (a full / read-only volume) — Save's temp file can no
	// longer be created — then render a different body.
	_ = os.RemoveAll(dir)
	st.setResult(1, api.DiffResult{PRNumber: 1, HeadSHA: "b", Resources: []api.DiffResource{{ID: "r0"}, {ID: "r1"}}})
	env2, _ := st.get(1)

	if env1.Digest == 0 || env2.Digest == 0 {
		t.Fatal("expected a non-zero content digest after each render")
	}
	if env1.Digest == env2.Digest {
		t.Fatal("the ETag digest must change when the diff body changes, even if the persist write failed")
	}
}

// #5 — a save that lands off-lock for a PR that was removed and reopened as a
// fresh job must neither clobber the replacement's savedDigest nor leave a zombie
// file a restart resurrects.
func TestStore_StaleSaveDoesNotClobberOrOrphanReplacedPR(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, err := persist.New(dir, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	st := newStore()
	st.loadFrom(p, discardLog())

	// A stale render's save lands off the lock after PR #7 was removed and reopened
	// as a fresh job. persistRecord (prev=0 forces the write) must neither clobber
	// the replacement job's savedDigest with the stale hash nor leave its own
	// now-orphaned file for a restart to resurrect.
	stale := &job{pr: api.PR{Number: 7, Open: true}, status: api.JobReady}
	st.markOpen(api.PR{Number: 7, Open: true}, false) // the live replacement job for #7
	rec := persist.Record{
		PR:     api.PR{Number: 7, Open: true},
		Status: api.JobReady,
		Result: &api.DiffResult{PRNumber: 7, HeadSHA: "a"},
	}
	st.persistRecord(stale, rec, 0)

	if live := st.jobs[7]; live == nil || live.savedDigest != 0 {
		t.Fatalf("stale save clobbered the replacement job's savedDigest: %+v", live)
	}
	fresh, err := persist.New(dir, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fresh.Load() {
		if r.PR.Number == 7 {
			t.Fatal("persistRecord left a zombie file for a replaced PR")
		}
	}
}

// #1 — a head-gone render on a still-open PR (the just-opened pull-ref race) must
// retry rather than wedge the PR in JobRunning forever.
func TestServer_HeadGoneOnOpenPRRetriesInsteadOfWedging(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		if calls.Add(1) == 1 {
			return api.DiffResult{}, gitclone.ErrHeadRefGone // pull ref not yet materialized
		}
		return api.DiffResult{PRNumber: pr.Number, HeadSHA: "abc"}, nil
	}}
	pr := api.PR{Number: 1, Open: true, HeadRef: "f", BaseRef: "main"}
	prov := &fakeProvider{prs: []api.PR{pr}} // GetPR reports it open — a transient race, not a close
	s := newTestServer(t, ghCfg("tok"), prov, eng)

	s.store.upsertPR(pr, false)
	s.queue.enqueue(pr)

	env := waitFor(t, s, 1)
	if env.Status != api.JobReady {
		t.Fatalf("head-gone on an open PR should retry to JobReady, got %s", env.Status)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("expected a re-render after the transient head-gone, got %d render calls", n)
	}
}
