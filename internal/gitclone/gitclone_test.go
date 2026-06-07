package gitclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, wt *git.Worktree, msg string) {
	t.Helper()
	if _, err := wt.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "t@example.com", When: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
}

// buildRepo creates a repo whose history forks:
//
//	C0 (main) ── C1 (main, adds other.yaml)
//	  └─ C2 (feature, changes config.yaml)
//
// merge-base(feature, main) == C0, so a correct base tree has the original
// config and NO other.yaml (that only exists on main's tip).
func buildRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	write(t, dir, "app/config.yaml", "replicas: 3\n")
	commit(t, wt, "C0: init") // C0 on the default branch

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	feature := plumbing.NewBranchReferenceName("feature")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(feature, head.Hash())); err != nil {
		t.Fatal(err)
	}

	write(t, dir, "app/other.yaml", "x: 1\n")
	commit(t, wt, "C1: main diverges") // C1 on the default branch

	if err := wt.Checkout(&git.CheckoutOptions{Branch: feature}); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "app/config.yaml", "replicas: 5\n")
	commit(t, wt, "C2: feature change") // C2 on feature

	return dir
}

func TestWithinRoot(t *testing.T) {
	t.Parallel()
	const dir = "/tmp/extract"
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/extract/app/config.yaml", true},
		{"/tmp/extract/a/b/c", true},
		{"/tmp/extract", true},
		{"/tmp/extract/../evil", false},      // single escape
		{"/tmp/extract/a/../../evil", false}, // escape after descending
		{"/tmp/evil", false},                 // sibling
		{"/etc/passwd", false},               // absolute escape
	}
	for _, tt := range cases {
		if got := withinRoot(dir, tt.path); got != tt.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", dir, tt.path, got, tt.want)
		}
	}
}

// newTestMirror builds a Mirror with fresh temp dirs for its bare repo and
// working trees, pointed at the local source repo src.
func newTestMirror(t *testing.T, src string) *Mirror {
	t.Helper()
	return NewMirror(t.TempDir(), t.TempDir(), src, "")
}

func read(dir, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	return string(b), err == nil
}

// assertMergeBaseTrees checks that res holds the feature tip as head and the
// merge-base (C0) — not main's tip — as base.
func assertMergeBaseTrees(t *testing.T, res *Result) {
	t.Helper()
	// head tree = feature tip (C2): config changed to 5
	if got, ok := read(res.HeadDir, "app/config.yaml"); !ok || got != "replicas: 5\n" {
		t.Errorf("head config.yaml = %q (ok=%v), want %q", got, ok, "replicas: 5\n")
	}
	// base tree = merge-base (C0): original config, NOT main's tip
	if got, ok := read(res.BaseDir, "app/config.yaml"); !ok || got != "replicas: 3\n" {
		t.Errorf("base config.yaml = %q (ok=%v), want %q", got, ok, "replicas: 3\n")
	}
	// the decisive check: other.yaml was added on main AFTER the fork, so it
	// must be absent from the merge-base tree (present only if we wrongly used
	// main's tip).
	if _, ok := read(res.BaseDir, "app/other.yaml"); ok {
		t.Error("base tree contains other.yaml — diff is against main's tip, not the merge-base")
	}
}

// TestMirror_HeadRefGone verifies that a pull head ref the remote no longer
// advertises (the request was deleted) surfaces as ErrHeadRefGone, so the server
// can reconcile the PR instead of recording a render failure.
func TestMirror_HeadRefGone(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)

	// "master" (the base) exists; pull request 999 does not, so its head ref is
	// absent — fetching it must report the ref as gone rather than an opaque error.
	_, err := newTestMirror(t, src).Trees(context.Background(), "refs/pull/999/head", "master")
	if !errors.Is(err, ErrHeadRefGone) {
		t.Fatalf("Trees with a missing head ref: got %v, want ErrHeadRefGone", err)
	}
}

// TestMirror_ForkPullHead verifies the head is fetched via the forge's pull head
// ref (refs/pull/N/head) — the ref the base repo publishes for a cross-repo
// (fork) PR, whose branch never lands in the base repo's refs/heads — so such
// PRs still render.
func TestMirror_ForkPullHead(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)

	// Publish the head at refs/pull/1/head (as a forge does for a fork PR) and
	// delete refs/heads/feature, so the pull ref is the only way to reach the head.
	repo, err := git.PlainOpen(src)
	if err != nil {
		t.Fatal(err)
	}
	feat, err := repo.Reference(plumbing.NewBranchReferenceName("feature"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference("refs/pull/1/head", feat.Hash())); err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("feature")); err != nil {
		t.Fatal(err)
	}

	res, err := newTestMirror(t, src).Trees(context.Background(), "refs/pull/1/head", "master")
	if err != nil {
		t.Fatalf("Trees via pull head ref: %v", err)
	}
	defer res.Cleanup()
	assertMergeBaseTrees(t, res)
}

func TestMirror_MergeBaseTrees(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)

	// the default branch from PlainInit is "master"
	res, err := newTestMirror(t, src).Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("Trees: %v", err)
	}
	t.Cleanup(res.Cleanup)
	assertMergeBaseTrees(t, res)
}

// TestMirror_Reuse confirms a second render against the same mirror reuses the
// existing bare repo (incremental fetch) rather than re-cloning, and still
// extracts correct trees. The bare repo must persist between calls.
func TestMirror_Reuse(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)
	m := newTestMirror(t, src)

	first, err := m.Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("Trees (first): %v", err)
	}
	t.Cleanup(first.Cleanup)
	if _, err := os.Stat(m.bareDir); err != nil {
		t.Fatalf("mirror bare repo not present after first render: %v", err)
	}

	second, err := m.Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("Trees (reuse): %v", err)
	}
	t.Cleanup(second.Cleanup)
	assertMergeBaseTrees(t, second)
	// Distinct working dirs each render; the persistent bare repo is shared.
	if first.HeadDir == second.HeadDir {
		t.Error("expected a fresh working tree per render")
	}
}

// TestMirror_Concurrent runs several renders against one mirror at once: the
// fetch/extract locking must neither deadlock nor race (run with -race), and
// every render must still get the correct trees.
func TestMirror_Concurrent(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)
	m := newTestMirror(t, src)

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			res, err := m.Trees(context.Background(), "refs/heads/feature", "master")
			if err != nil {
				errs[i] = err
				return
			}
			defer res.Cleanup()
			if got, ok := read(res.HeadDir, "app/config.yaml"); !ok || got != "replicas: 5\n" {
				errs[i] = fmt.Errorf("head config.yaml = %q (ok=%v)", got, ok)
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent render %d: %v", i, err)
		}
	}
}

// TestMirror_SkipsOversizedFiles verifies extractTree drops blobs over
// maxFileBytes (a hostile or accidental giant file) while still extracting the
// normal ones.
func TestMirror_SkipsOversizedFiles(t *testing.T) {
	orig := maxFileBytes
	maxFileBytes = 16
	t.Cleanup(func() { maxFileBytes = orig })

	src := t.TempDir()
	repo, err := git.PlainInit(src, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	write(t, src, "small.yaml", "a: 1\n")               // under the cap
	write(t, src, "big.yaml", strings.Repeat("x", 100)) // over the cap
	commit(t, wt, "C0")
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("feature"), head.Hash()),
	); err != nil {
		t.Fatal(err)
	}

	res, err := newTestMirror(t, src).Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("Trees: %v", err)
	}
	t.Cleanup(res.Cleanup)

	if _, ok := read(res.HeadDir, "small.yaml"); !ok {
		t.Error("small.yaml is under the cap and should be extracted")
	}
	if _, ok := read(res.HeadDir, "big.yaml"); ok {
		t.Error("big.yaml exceeds maxFileBytes and should be skipped")
	}
}
