package persist

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/api"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	p, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	p := newTestStore(t)
	when := time.Now().Truncate(time.Second).UTC()
	rec := Record{
		PR:       api.PR{Number: 142, Title: "feat(rook): bump", HeadSHA: "abc1234", Merged: true},
		Status:   api.JobReady,
		Result:   &api.DiffResult{PRNumber: 142, HeadSHA: "abc1234"},
		Updated:  when,
		ClosedAt: when,
	}
	if err := p.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := p.Load()
	if len(got) != 1 {
		t.Fatalf("Load returned %d records, want 1", len(got))
	}
	r := got[0]
	if r.PR.Number != 142 || r.PR.Title != rec.PR.Title || r.Status != api.JobReady {
		t.Fatalf("metadata not round-tripped: %+v", r)
	}
	if r.Result == nil || r.Result.HeadSHA != "abc1234" {
		t.Fatalf("diff not round-tripped: %+v", r.Result)
	}
	if !r.ClosedAt.Equal(when) {
		t.Errorf("ClosedAt = %v, want %v", r.ClosedAt, when)
	}
}

func TestSaveOverwrites(t *testing.T) {
	t.Parallel()
	p := newTestStore(t)
	for _, sha := range []string{"old", "new"} {
		if err := p.Save(Record{PR: api.PR{Number: 7, HeadSHA: sha}, Status: api.JobReady}); err != nil {
			t.Fatalf("Save(%s): %v", sha, err)
		}
	}
	got := p.Load()
	if len(got) != 1 {
		t.Fatalf("Load returned %d records, want 1 (overwrite)", len(got))
	}
	if got[0].PR.HeadSHA != "new" {
		t.Fatalf("HeadSHA = %q, want newest write", got[0].PR.HeadSHA)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	p := newTestStore(t)
	if err := p.Save(Record{PR: api.PR{Number: 5}, Status: api.JobReady}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := p.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := p.Load(); len(got) != 0 {
		t.Fatalf("Load after Delete returned %d records, want 0", len(got))
	}
	// Deleting an absent PR is not an error.
	if err := p.Delete(5); err != nil {
		t.Errorf("Delete(absent) = %v, want nil", err)
	}
}

func TestLoadSkipsCorrupt(t *testing.T) {
	t.Parallel()
	p := newTestStore(t)
	if err := p.Save(Record{PR: api.PR{Number: 1}, Status: api.JobReady}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A file with the right suffix but garbage (non-gzip) contents must be
	// skipped, not crash the load.
	if err := os.WriteFile(filepath.Join(p.dir, "99"+fileSuffix), []byte("not gzip"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	got := p.Load()
	if len(got) != 1 || got[0].PR.Number != 1 {
		t.Fatalf("Load should skip the corrupt file and return only #1; got %+v", got)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	t.Parallel()
	if got := newTestStore(t).Load(); len(got) != 0 {
		t.Fatalf("Load of an empty dir returned %d records, want 0", len(got))
	}
}
