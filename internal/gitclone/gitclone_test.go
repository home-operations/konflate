package gitclone

import (
	"context"
	"os"
	"path/filepath"
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

func TestClone_MergeBaseTrees(t *testing.T) {
	t.Parallel()
	src := buildRepo(t)

	// the default branch from PlainInit is "master"
	res, err := Clone(context.Background(), src, "", "feature", "master")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(res.Cleanup)

	read := func(dir, rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		return string(b), err == nil
	}

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
