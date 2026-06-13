package gitclone

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// TestMirror_RepacksWhenPacksAccumulate verifies the mirror folds its packfiles
// into one once they pile up past the threshold. go-git appends a pack per fetch
// and never gc's them, so without the repack the bare repo would grow one pack
// per render forever. Each render here advances the head branch so the fetch
// actually transfers objects (and thus writes a new pack); after enough renders
// to cross the lowered threshold, the pack count must collapse — and renders must
// still resolve correct trees off the consolidated pack.
func TestMirror_RepacksWhenPacksAccumulate(t *testing.T) {
	orig := repackPackThreshold
	repackPackThreshold = 3
	t.Cleanup(func() { repackPackThreshold = orig })

	src := buildRepo(t)
	repo, err := git.PlainOpen(src)
	if err != nil {
		t.Fatal(err)
	}
	// buildRepo leaves the worktree checked out on "feature"; keep committing
	// there so each fetch of refs/heads/feature pulls a fresh object set.
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	m := newTestMirror(t, src)
	const renders = 6 // > threshold+1, so at least one fetch crosses the bound
	var last *Result
	for i := range renders {
		write(t, src, "app/config.yaml", fmt.Sprintf("replicas: %d\n", 10+i))
		commit(t, wt, fmt.Sprintf("advance feature %d", i))

		if last != nil {
			last.Cleanup()
		}
		last, err = m.Trees(context.Background(), "refs/heads/feature", "master")
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
	}
	t.Cleanup(last.Cleanup)

	// The repack must keep the pack count bounded. Without it, `renders`
	// object-transferring fetches would leave roughly renders+1 packs.
	n, err := m.countPacks()
	if err != nil {
		t.Fatal(err)
	}
	if n > repackPackThreshold {
		t.Errorf("pack count = %d after %d renders; repack should keep it ≤ %d", n, renders, repackPackThreshold)
	}

	// And renders must still resolve correct trees off the consolidated pack:
	// head is feature's latest tip, base is the merge-base (C0), not main's tip.
	wantHead := fmt.Sprintf("replicas: %d\n", 10+renders-1)
	if got, ok := read(last.HeadDir, "app/config.yaml"); !ok || got != wantHead {
		t.Errorf("head config.yaml = %q (ok=%v) after repack, want %q", got, ok, wantHead)
	}
	if got, ok := read(last.BaseDir, "app/config.yaml"); !ok || got != "replicas: 3\n" {
		t.Errorf("base config.yaml = %q (ok=%v) after repack, want merge-base %q", got, ok, "replicas: 3\n")
	}
	if _, ok := read(last.BaseDir, "app/other.yaml"); ok {
		t.Error("base tree contains other.yaml after repack — merge-base resolution broke")
	}
}

// TestMirror_RebuildsOnCorruptObjectStore reproduces the production wedge — a
// repack whose ref walk hits a missing object ("getting object <sha> failed:
// object not found") — and verifies the mirror self-heals: it discards the
// damaged bare repo, re-seeds from a clean clone, and the render still resolves
// correct trees. Without the rebuild, the corrupt mirror would fail every render
// forever (go-git's incremental fetch can't re-fetch the missing ancestors,
// since it advertises the broken refs as haves).
func TestMirror_RebuildsOnCorruptObjectStore(t *testing.T) {
	orig := repackPackThreshold
	repackPackThreshold = 1 // force a repack on the next render
	t.Cleanup(func() { repackPackThreshold = orig })

	src := buildRepo(t)
	m := newTestMirror(t, src)

	// First render seeds the mirror.
	first, err := m.Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("seed render: %v", err)
	}
	first.Cleanup()

	// Corrupt the mirror: add a ref pointing at a fabricated, absent object.
	// RepackObjects walks every ref, so the next repack tries to read it and fails
	// with exactly the production error — without having to surgically edit packs.
	bare, err := git.PlainOpen(m.bareDir)
	if err != nil {
		t.Fatal(err)
	}
	ghost := plumbing.NewHash("0e2d2fd5a618d41d4da42a9d7ebf1a14659dd6c9")
	if err := bare.Storer.SetReference(
		plumbing.NewHashReference("refs/heads/ghost", ghost),
	); err != nil {
		t.Fatal(err)
	}

	// Second render must self-heal (rebuild) and still return correct trees.
	res, err := m.Trees(context.Background(), "refs/heads/feature", "master")
	if err != nil {
		t.Fatalf("render after corruption should self-heal, got: %v", err)
	}
	t.Cleanup(res.Cleanup)
	assertMergeBaseTrees(t, res)

	// The rebuild re-seeded from a clean clone, so the dangling ref is gone —
	// proof the mirror was actually rebuilt, not that the error was swallowed.
	rebuilt, err := git.PlainOpen(m.bareDir)
	if err != nil {
		t.Fatalf("reopen rebuilt mirror: %v", err)
	}
	if _, err := rebuilt.Reference("refs/heads/ghost", false); err == nil {
		t.Error("dangling ref survived the rebuild; mirror was not re-seeded")
	}
}

// TestMirrorCorrupt covers the classifier that decides whether a failed fetch
// means the mirror's object store is damaged (rebuild it) versus a transient or
// expected failure (leave it alone). The exclusions matter most: a re-clone
// fired on a network blip or a gone head ref would be wasteful or wrong.
func TestMirrorCorrupt(t *testing.T) {
	t.Parallel()
	// netEOF is a network error whose message also matches a corruption
	// signature ("unexpected EOF") — the exclusion must win, so a flaky link
	// doesn't masquerade as a truncated object.
	netEOF := &net.OpError{Op: "read", Err: errors.New("unexpected EOF")}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// Damaged object store — rebuild.
		{"object not found (the reported wedge)", fmt.Errorf("gitclone: repack mirror: getting object abc failed: %w", plumbing.ErrObjectNotFound), true},
		{"object not found bare", plumbing.ErrObjectNotFound, true},
		{"object corrupt", errors.New("object corrupt: bad header"), true},
		{"packfile is corrupt", errors.New("packfile is corrupt"), true},
		{"zlib invalid header", errors.New("zlib: invalid header"), true},
		{"invalid checksum", errors.New("pack: invalid checksum"), true},
		{"unexpected EOF (truncated object)", errors.New("unexpected EOF"), true},
		// Transient or expected — leave the mirror alone.
		{"gone head ref", fmt.Errorf("gitclone: fetch head: %w", ErrHeadRefGone), false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", fmt.Errorf("fetch: %w", context.DeadlineExceeded), false},
		{"network error carrying unexpected EOF", netEOF, false},
		{"unrelated error", errors.New("permission denied"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mirrorCorrupt(tc.err); got != tc.want {
				t.Errorf("mirrorCorrupt(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
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

// TestMirror_AuthFor covers the per-fetch credential resolution: a nil or
// empty-yielding token source clones anonymously (nil auth), a yielded token
// becomes x-access-token basic auth (the password over HTTPS), and a source
// error — e.g. a GitHub App token mint that failed — propagates rather than
// silently degrading to an anonymous clone of a private repo.
func TestMirror_AuthFor(t *testing.T) {
	t.Parallel()
	const tok = "ghs_installationtoken"
	cases := []struct {
		name              string
		token             TokenFunc
		wantUser, wantPro string // empty wantUser ⇒ expect nil auth (anonymous)
		wantErr           bool
	}{
		{"nil source is anonymous", nil, "", "", false},
		{"empty token is anonymous", func(context.Context) (string, error) { return "", nil }, "", "", false},
		{"token becomes x-access-token basic auth", func(context.Context) (string, error) { return tok, nil }, "x-access-token", tok, false},
		{"source error propagates", func(context.Context) (string, error) { return "", errors.New("mint failed") }, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMirror(t.TempDir(), t.TempDir(), "https://example.test/x.git", tc.token, 0)
			auth, err := m.authFor(context.Background())
			switch {
			case tc.wantErr:
				if err == nil {
					t.Fatal("authFor: want error, got nil")
				}
			case err != nil:
				t.Fatalf("authFor: %v", err)
			case tc.wantUser == "":
				if auth != nil {
					t.Errorf("authFor = %+v, want nil (anonymous)", auth)
				}
			case auth == nil || auth.Username != tc.wantUser || auth.Password != tc.wantPro:
				t.Errorf("authFor = %+v, want {%q, %q}", auth, tc.wantUser, tc.wantPro)
			}
		})
	}
}

// newTestMirror builds a Mirror with fresh temp dirs for its bare repo and
// working trees, pointed at the local source repo src.
func newTestMirror(t *testing.T, src string) *Mirror {
	t.Helper()
	return NewMirror(t.TempDir(), t.TempDir(), src, nil, 0) // nil token: anonymous; 0: fetch bounded only by ctx
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

// TestMirror_FetchScope verifies the per-fetch deadline: fetchTimeout bounds the
// fetch, but a tighter caller deadline always wins (so a fetch can never outlast
// the end-to-end DiffTimeout budget). This bound is what stops a slow or hung
// fetch from holding the write lock — and starving every other render — longer
// than intended.
func TestMirror_FetchScope(t *testing.T) {
	t.Parallel()

	// Disabled (<=0): no deadline imposed when the caller has none.
	disabled := &Mirror{fetchTimeout: 0}
	ctx, cancel := disabled.fetchScope(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("fetchTimeout<=0 must not impose a deadline")
	}

	// Enabled: a deadline of roughly now+fetchTimeout is imposed.
	enabled := &Mirror{fetchTimeout: time.Hour}
	ctx, cancel = enabled.fetchScope(context.Background())
	defer cancel()
	if dl, ok := ctx.Deadline(); !ok {
		t.Error("fetchTimeout>0 must impose a deadline")
	} else if d := time.Until(dl); d <= 0 || d > time.Hour+time.Minute {
		t.Errorf("fetch deadline %v outside the expected ~1h window", d)
	}

	// A tighter caller deadline wins: fetchTimeout never loosens it.
	parent, pcancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer pcancel()
	ctx, cancel = enabled.fetchScope(parent) // fetchTimeout 1h vs parent 10ms
	defer cancel()
	if dl, ok := ctx.Deadline(); !ok {
		t.Error("expected the tighter parent deadline to carry through")
	} else if d := time.Until(dl); d > time.Minute {
		t.Errorf("expected the ~10ms parent deadline to win, got %v", d)
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

// TestMirror_ExtractsNestedDirectories guards the per-directory MkdirAll dedupe
// in extractTree: files spread across several directories (two in the same dir,
// plus a deeper nesting) must all materialize with their contents. A MkdirAll
// wrongly skipped for a genuinely new directory would drop that directory's
// files, so this exercises directory transitions, not just same-dir runs.
func TestMirror_ExtractsNestedDirectories(t *testing.T) {
	src := t.TempDir()
	repo, err := git.PlainInit(src, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"root.yaml":       "r: 0\n",
		"app/one.yaml":    "a: 1\n",
		"app/two.yaml":    "a: 2\n", // second file in app/ — the dedupe's common case
		"infra/net.yaml":  "i: 1\n", // a new directory after app/
		"app/deep/x.yaml": "d: 1\n", // a deeper nesting
	}
	for rel, content := range files {
		write(t, src, rel, content)
	}
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

	for rel, want := range files {
		if got, ok := read(res.HeadDir, rel); !ok || got != want {
			t.Errorf("%s = %q (ok=%v), want %q", rel, got, ok, want)
		}
	}
}

// TestMirror_ExtractsSymlinkAsRegularFile verifies a symlink committed to the
// repo is materialized as a regular file whose contents are the link target
// string — never a live symlink. go-git's blob reader yields the blob (the
// target path), and extractTree streams it to a regular file, so flate later
// reads inert text instead of following a link out of the tree. A hostile repo
// konflate doesn't own could otherwise smuggle host files (e.g. a link to
// /etc/passwd, or one escaping the extraction root) into a render.
func TestMirror_ExtractsSymlinkAsRegularFile(t *testing.T) {
	src := t.TempDir()
	repo, err := git.PlainInit(src, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	write(t, src, "real.yaml", "a: 1\n")
	// A symlink whose target escapes the extraction root. If extractTree created
	// a real symlink, a reader following it would leave the tree; as a regular
	// file the target is harmless text.
	if err := os.Symlink("../../../../etc/passwd", filepath.Join(src, "link.yaml")); err != nil {
		t.Fatal(err)
	}
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

	res, err := newTestMirror(t, src).Trees(context.Background(), "feature", "master")
	if err != nil {
		t.Fatalf("Trees: %v", err)
	}
	t.Cleanup(res.Cleanup)

	info, err := os.Lstat(filepath.Join(res.HeadDir, "link.yaml"))
	if err != nil {
		t.Fatalf("symlink entry was not extracted: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("symlink was materialized as a live symlink; it must be a regular file")
	}
	// The regular file holds the link target text, not whatever it pointed at.
	if got, _ := read(res.HeadDir, "link.yaml"); got != "../../../../etc/passwd" {
		t.Errorf("extracted symlink content = %q, want the inert target string", got)
	}
}
