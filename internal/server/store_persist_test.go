package server

import (
	"testing"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/persist"
)

// TestStore_PersistsAcrossRestart simulates a pod restart: a store that wrote
// its rendered diffs to a state dir is replaced by a fresh store pointed at the
// same dir, and the open PR (with its diff) and the merged shelf both come back
// — exactly what issue #80 asks for. Removing/pruning evicts from disk too.
func TestStore_PersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	open := func() *store {
		p, err := persist.New(dir, discardLog())
		if err != nil {
			t.Fatalf("persist.New: %v", err)
		}
		s := newStore()
		s.loadFrom(p, discardLog())
		return s
	}

	// First process: render an open PR (#1) and render-then-merge another (#2).
	s1 := open()
	s1.upsertPR(api.PR{Number: 1, HeadSHA: "a", Open: true})
	s1.setResult(1, api.DiffResult{PRNumber: 1, HeadSHA: "a", Warnings: []api.Warning{{}}})
	s1.upsertPR(api.PR{Number: 2, HeadSHA: "b", Open: true})
	s1.setResult(2, api.DiffResult{PRNumber: 2, HeadSHA: "b"})
	s1.markClosed(2, s1.now())

	// Second process: a brand-new store loads from the same state dir.
	s2 := open()
	if got := s2.list(); len(got) != 2 {
		t.Fatalf("restored %d PRs, want 2", len(got))
	}

	// #1 is back open, ready, with its diff — and its recomputed signals.
	e1, ok := s2.get(1)
	if !ok || e1.Status != api.JobReady || e1.Diff == nil || e1.Diff.HeadSHA != "a" {
		t.Fatalf("PR #1 not restored with its diff: ok=%v %+v", ok, e1)
	}
	if pr1 := prStatus(s2, 1); pr1.ClosedAt != nil || pr1.Signals == nil || pr1.Signals.Caution != 1 {
		t.Fatalf("PR #1 should be open with recomputed signals; got %+v", pr1)
	}

	// #2 is back on the merged shelf (ClosedAt set), frozen at its diff.
	if pr2 := prStatus(s2, 2); pr2.ClosedAt == nil || !pr2.Merged {
		t.Fatalf("PR #2 should be restored as merged; got %+v", pr2)
	}

	// remove() also deletes the file, so a later restart doesn't resurrect it.
	s2.remove(1)
	if _, ok := open().get(1); ok {
		t.Fatal("a removed PR must not reload after a restart")
	}
}

// prStatus finds a PR in the store's list (helper for the test).
func prStatus(s *store, number int) api.PRStatus {
	for _, p := range s.list() {
		if p.Number == number {
			return p
		}
	}
	return api.PRStatus{}
}
