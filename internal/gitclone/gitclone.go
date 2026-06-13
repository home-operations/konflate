// Package gitclone fetches a repository and materializes the two trees a PR
// diff needs: the PR head, and the merge-base of the head and the target
// branch (GitHub-style — so changes that landed on the base branch after the
// PR opened don't pollute the diff). Pure go-git; no `git` CLI shellout.
//
// konflate tracks a single repository, so rather than re-cloning it for every
// render, a [Mirror] keeps one persistent bare repo on disk and fetches just
// the base + head refs each render — an incremental fetch instead of a full
// clone. The bare repo lives under the cache dir, so it also survives restarts.
package gitclone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ErrHeadRefGone reports that the PR's head ref no longer exists on the remote —
// the pull/merge request was deleted, so the forge dropped its head ref. Callers
// treat this as "the PR left the open set", not a render failure. (A merge or
// close alone no longer trips this: the forge keeps refs/pull/N/head after merge,
// and such PRs are reconciled off the open set by the periodic refresh instead.)
var ErrHeadRefGone = errors.New("gitclone: head ref no longer exists on remote")

// localHeadRef is the mirror-local ref the PR head is fetched into. The head is
// taken from the forge's pull head ref (refs/pull/N/head), which isn't a branch,
// so it's mapped into this private namespace rather than refs/heads. Fetches are
// serialized under Mirror.mu, and each render resolves its head commit before
// releasing the lock, so one shared local ref is safe.
const localHeadRef = "refs/konflate/pull-head"

// Result holds the two extracted working trees plus a cleanup func that
// removes them. Callers must call Cleanup.
type Result struct {
	BaseDir string // merge-base(head, base) tree
	HeadDir string // head tree
	Cleanup func()
}

// Mirror is a persistent bare clone of one repository. Trees fetches the
// requested refs into it (incrementally) and extracts the two trees a diff
// needs. It is safe for concurrent use: fetches are serialized and excluded
// from the object reads done while extracting (go-git fetch appends to the
// object store, so a read racing a write could otherwise see a torn ref).
type Mirror struct {
	bareDir  string // persistent bare repo (under the cache dir)
	workRoot string // parent for the ephemeral per-diff working trees
	url      string
	token    TokenFunc // forge credential for clone/fetch, resolved per fetch (nil ⇒ anonymous)

	// fetchTimeout bounds a single fetch (and the cold clone) so a slow or hung
	// forge can't hold the write lock — and thus block every other render — for
	// longer than this. <=0 leaves the fetch bounded only by the caller's ctx.
	fetchTimeout time.Duration

	// fetch takes the write lock; merge-base + tree extraction take the read
	// lock, so several renders extract concurrently but none does so while a
	// fetch is mutating the shared object store.
	mu sync.RWMutex
}

// TokenFunc yields the forge credential for git-over-HTTPS — the password paired
// with the x-access-token username — resolved afresh before each fetch so a
// GitHub App's hourly-expiring installation token stays current. An empty token
// (or a nil TokenFunc) means anonymous. It receives the fetch's context so the
// resolution (an App token mint, on first use or refresh) is bounded with it.
type TokenFunc func(ctx context.Context) (string, error)

// NewMirror builds a Mirror for cloneURL. The bare repo lives under cacheDir
// (persistent), per-diff working trees under cloneDir (ephemeral). token may be
// nil for public repositories (anonymous). fetchTimeout bounds each fetch under
// the write lock (<=0 disables it; see Mirror.fetchTimeout).
func NewMirror(cacheDir, cloneDir, cloneURL string, token TokenFunc, fetchTimeout time.Duration) *Mirror {
	return &Mirror{
		bareDir:      filepath.Join(cacheDir, "git", "mirror.git"),
		workRoot:     cloneDir,
		url:          cloneURL,
		token:        token,
		fetchTimeout: fetchTimeout,
	}
}

// authFor resolves the git credential for one fetch cycle by calling the token
// source (a GitHub App mints/refreshes its installation token here), returning
// nil — anonymous — when there is no source or it yields an empty token. The same
// credential covers the cycle's clone and both ref fetches.
func (m *Mirror) authFor(ctx context.Context) (*githttp.BasicAuth, error) {
	if m.token == nil {
		return nil, nil
	}
	tok, err := m.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitclone: forge credential: %w", err)
	}
	if tok == "" {
		return nil, nil
	}
	// Any non-empty username works; the token is the password over HTTPS.
	return &githttp.BasicAuth{Username: "x-access-token", Password: tok}, nil
}

// Trees fetches the PR head and base into the mirror, computes the merge-base of
// the two (GitHub-style), and extracts the head and merge-base trees to a fresh
// working directory. headRef is a full server-side ref — the forge's pull head
// ref (refs/pull/N/head), which the base repo publishes for fork and same-repo
// PRs alike — and baseRef is the target branch name. Callers must call
// Result.Cleanup.
func (m *Mirror) Trees(ctx context.Context, headRef, baseRef string) (_ *Result, err error) {
	// Bound the fetch on its own — it runs under the write lock, so a slow or hung
	// fetch would otherwise block every other render for the full caller deadline.
	// The merge-base walk and tree extraction below are local and stay on ctx.
	fetchCtx, cancel := m.fetchScope(ctx)
	defer cancel()
	head, base, err := m.fetch(fetchCtx, headRef, baseRef)
	if err != nil {
		return nil, err
	}

	// Prepare the ephemeral work dir before taking the read lock — directory
	// creation doesn't touch the mirror, so holding the lock for it would only
	// widen the window a fetcher waits on the write lock.
	if err := os.MkdirAll(m.workRoot, 0o755); err != nil {
		return nil, fmt.Errorf("gitclone: clone dir: %w", err)
	}
	root, err := os.MkdirTemp(m.workRoot, "diff-")
	if err != nil {
		return nil, fmt.Errorf("gitclone: work dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	// Object reads (merge-base walk + tree extraction) run under the read lock
	// so no concurrent fetch mutates the store mid-read. The commit objects were
	// resolved during fetch and stay valid here.
	m.mu.RLock()
	defer m.mu.RUnlock()

	mergeBase := base
	bases, err := head.MergeBase(base)
	if err != nil {
		return nil, fmt.Errorf("gitclone: merge-base: %w", err)
	}
	if len(bases) > 0 {
		mergeBase = bases[0]
	}

	res := &Result{
		BaseDir: filepath.Join(root, "base"),
		HeadDir: filepath.Join(root, "head"),
		Cleanup: cleanup,
	}
	if err = extractTree(mergeBase, res.BaseDir); err != nil {
		return nil, fmt.Errorf("gitclone: extract merge-base: %w", err)
	}
	if err = extractTree(head, res.HeadDir); err != nil {
		return nil, fmt.Errorf("gitclone: extract head: %w", err)
	}
	return res, nil
}

// fetchScope derives the context that bounds a single fetch. With fetchTimeout
// set it is the tighter of the caller's deadline and now+fetchTimeout (so it can
// never outlast the end-to-end DiffTimeout budget); otherwise it is the caller's
// context unchanged. The returned cancel must always be called.
func (m *Mirror) fetchScope(ctx context.Context) (context.Context, context.CancelFunc) {
	if m.fetchTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, m.fetchTimeout)
}

// fetch ensures the bare mirror exists (cloning it once), fetches the base and
// head refs into it, and resolves both to commits — all under the write lock so
// only one render mutates the store at a time. The write lock is why fetch is
// bounded by its own (short) timeout: a slow or hung fetch holding it blocks
// every other render's Trees call, so the bound caps that head-of-line blocking
// instead of letting it run to the full DiffTimeout. The returned commits carry
// their own storer reference, so they stay readable after the lock is released.
func (m *Mirror) fetch(ctx context.Context, headRef, baseRef string) (head, base *object.Commit, err error) {
	// Resolve the credential before taking the write lock: an App token mint is a
	// network call, and holding the lock across it would stall every other render.
	// Once minted it's cached, so subsequent cycles resolve without I/O.
	auth, err := m.authFor(ctx)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	head, base, err = m.fetchLocked(ctx, headRef, baseRef, auth)
	if err == nil || !mirrorCorrupt(err) {
		return head, base, err
	}
	// The persistent mirror's object store is inconsistent — a ref-reachable
	// object is missing or unreadable, so the repack walk (or a later read) fails.
	// go-git's incremental fetch can't repair it: it advertises the broken refs as
	// haves, so the forge replies "up to date" and never re-sends the missing
	// ancestors. Discard the mirror and re-seed from a clean clone, then retry
	// once. Transient failures (network/auth/ctx) and an expected gone head ref are
	// excluded by mirrorCorrupt, so this only fires on real object-store damage.
	// Safe under the write lock: it excludes the read lock Trees holds while
	// extracting, so no render is mid-read of the store we're removing.
	slog.Default().Warn("rebuilding corrupt git mirror", "error", err, "dir", m.bareDir)
	if rmErr := os.RemoveAll(m.bareDir); rmErr != nil {
		return nil, nil, fmt.Errorf("gitclone: rebuild mirror: %w", rmErr)
	}
	return m.fetchLocked(ctx, headRef, baseRef, auth)
}

// fetchLocked performs one fetch cycle: open-or-seed the mirror, refresh the base
// and head refs, repack if the packs have piled up, and resolve both commits. The
// caller holds the write lock and has resolved auth. Split from fetch so the
// self-heal path can re-run the whole cycle against a freshly re-seeded mirror.
func (m *Mirror) fetchLocked(ctx context.Context, headRef, baseRef string, auth *githttp.BasicAuth) (head, base *object.Commit, err error) {
	repo, err := m.openOrClone(ctx, baseRef, auth)
	if err != nil {
		return nil, nil, err
	}

	// Refresh the base branch (a no-op right after the seeding clone). A missing
	// base is a real error — the PR targets it.
	baseFull := "refs/heads/" + baseRef
	if err := fetchSpec(ctx, repo, auth, baseFull, baseFull); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, nil, fmt.Errorf("gitclone: fetch base %q: %w", baseRef, err)
	}
	// headRef is the forge's pull head ref (refs/pull/N/head), which the base repo
	// publishes for every PR — including forks, whose branch isn't in the base
	// repo. Fetch it into a private local ref. A missing head ref means the
	// request was deleted — report it as gone so the caller reconciles the PR
	// rather than failing the render.
	if err := fetchSpec(ctx, repo, auth, headRef, localHeadRef); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if errors.Is(err, git.NoMatchingRefSpecError{}) {
			return nil, nil, fmt.Errorf("gitclone: fetch head %q: %w", headRef, ErrHeadRefGone)
		}
		return nil, nil, fmt.Errorf("gitclone: fetch head %q: %w", headRef, err)
	}

	// Each fetch above appends a packfile, and go-git never consolidates them on
	// its own (it has no `git gc`), so the mirror's pack count climbs by one per
	// render and its object store grows without bound. Once the packs pile up
	// past the threshold, fold them into one. The repack deletes the old packs,
	// which invalidates this handle's cached pack set, so re-open before resolving
	// commits off it.
	repacked, err := m.maybeRepack(repo)
	if err != nil {
		return nil, nil, err
	}
	if repacked {
		repo, err = git.PlainOpen(m.bareDir)
		if err != nil {
			return nil, nil, fmt.Errorf("gitclone: reopen mirror after repack: %w", err)
		}
	}

	head, err = resolveCommit(repo, localHeadRef)
	if err != nil {
		return nil, nil, fmt.Errorf("gitclone: head %q: %w", headRef, err)
	}
	base, err = resolveCommit(repo, baseRef)
	if err != nil {
		return nil, nil, fmt.Errorf("gitclone: base %q: %w", baseRef, err)
	}
	return head, base, nil
}

// mirrorCorrupt reports whether err means the bare mirror's object store is
// damaged — a ref-reachable object is missing or unreadable — so the mirror
// should be discarded and re-cloned rather than reused. This is distinct from a
// transient failure (network/auth/ctx) or an expected one (a gone head ref),
// which must not trigger a wasteful re-clone, so those are excluded first.
//
// Detection is by message signature, not errors.Is: go-git's object walker
// flattens the underlying plumbing.ErrObjectNotFound with %v
// (object_walker.go), breaking the error chain — so the canonical "repack
// mirror: getting object <sha> failed: object not found" can only be matched on
// its text.
func mirrorCorrupt(err error) bool {
	if err == nil {
		return false
	}
	// Expected (gone head ref) or caller-driven (deadline/cancel): not corruption.
	if errors.Is(err, ErrHeadRefGone) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	for _, sig := range mirrorCorruptSignatures {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// mirrorCorruptSignatures are the go-git error texts that mark a damaged object
// store. A var so it is easy to extend as new signatures surface.
var mirrorCorruptSignatures = []string{
	plumbing.ErrObjectNotFound.Error(), // "object not found" — the reported repack wedge
}

// repackPackThreshold is the packfile count at which a fetch consolidates the
// mirror into a single pack. go-git appends one pack per fetch and never runs
// the equivalent of `git gc`, so without an occasional repack the pack directory
// would grow one file per render forever — slowing every object lookup and
// bloating the cache volume. 50 matches git's gc.autoPackLimit default. A var,
// not a const, so tests can lower it.
var repackPackThreshold = 50

// maybeRepack folds the mirror's packfiles into one when they have piled up past
// repackPackThreshold, reporting whether it did. go-git's RepackObjects writes a
// single new pack holding every object reachable from the mirror's refs — so the
// unreachable objects a force-push leaves behind are reclaimed too — then deletes
// the superseded packs. That deletion invalidates the open handle's cached pack
// set, so the caller must re-open the mirror before reading objects through it.
// Caller holds the write lock.
func (m *Mirror) maybeRepack(repo *git.Repository) (bool, error) {
	n, err := m.countPacks()
	if err != nil {
		return false, err
	}
	if n < repackPackThreshold {
		return false, nil
	}
	// The zero RepackConfig uses OFS deltas and a zero OnlyDeletePacksOlderThan,
	// which imposes no age floor — every superseded pack is removed.
	if err := repo.RepackObjects(&git.RepackConfig{}); err != nil {
		return false, fmt.Errorf("gitclone: repack mirror: %w", err)
	}
	return true, nil
}

// countPacks returns the number of *.pack files in the mirror's object store. A
// missing pack directory (objects still loose, or none fetched yet) counts as zero.
func (m *Mirror) countPacks() (int, error) {
	entries, err := os.ReadDir(filepath.Join(m.bareDir, "objects", "pack"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("gitclone: read pack dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pack") {
			n++
		}
	}
	return n, nil
}

// openOrClone opens the bare mirror, seeding it with a single-branch bare clone
// of the base branch on first use. A bare, single-branch clone avoids pulling
// the repo's other (often many) branches; subsequent head/base refs are added
// by explicit fetches. Caller holds the write lock.
//
// Deliberately opens a fresh *git.Repository per call rather than caching one on
// the Mirror: go-git's storer mutates unsynchronized object-cache maps lazily on
// read, so a shared handle read by several renders under the (shared) read lock
// would data-race. A private handle per render keeps those reads goroutine-safe;
// don't "optimize" this into a cached handle without moving all object reads
// under the write lock.
func (m *Mirror) openOrClone(ctx context.Context, baseRef string, auth *githttp.BasicAuth) (*git.Repository, error) {
	repo, err := git.PlainOpen(m.bareDir)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("gitclone: open mirror: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.bareDir), 0o755); err != nil {
		return nil, fmt.Errorf("gitclone: mirror dir: %w", err)
	}
	repo, err = git.PlainCloneContext(ctx, m.bareDir, true, &git.CloneOptions{
		URL:           m.url,
		Auth:          auth,
		NoCheckout:    true,
		Tags:          git.NoTags,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(baseRef),
	})
	if err != nil {
		// A partial clone would wedge every later open; clear it so the next
		// render re-seeds cleanly.
		_ = os.RemoveAll(m.bareDir)
		return nil, fmt.Errorf("gitclone: clone %s (base %q): %w", m.url, baseRef, err)
	}
	return repo, nil
}

// fetchSpec force-fetches a single remote ref (src) into a local ref (dst),
// pulling only that ref (no tags). An explicit, forced refspec lets the mirror
// accumulate any base branch or pull head a PR needs, regardless of the ref it
// was seeded with, and overwrite a stale local copy after a force-push.
func fetchSpec(ctx context.Context, repo *git.Repository, auth *githttp.BasicAuth, src, dst string) error {
	spec := gitconfig.RefSpec(fmt.Sprintf("+%s:%s", src, dst))
	return repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     auth,
		RefSpecs: []gitconfig.RefSpec{spec},
		Tags:     git.NoTags,
		Force:    true,
	})
}

// resolveCommit resolves a branch name (or any revision) to its commit. It
// tries the bare-repo branch ref first, then a generic revision parse.
func resolveCommit(repo *git.Repository, ref string) (*object.Commit, error) {
	for _, rev := range []plumbing.Revision{
		plumbing.Revision(plumbing.NewBranchReferenceName(ref).String()),
		plumbing.Revision(ref),
	} {
		if h, err := repo.ResolveRevision(rev); err == nil {
			return repo.CommitObject(*h)
		}
	}
	return nil, fmt.Errorf("could not resolve %q", ref)
}

// maxFileBytes caps the size of a single file extracted from a tree. Flux
// manifests are small YAML; a giant blob (a committed binary, or a hostile repo
// konflate doesn't own) would only bloat memory and the cache volume — and
// isn't review-relevant — so oversized files are skipped. A var, not a const,
// so tests can lower it.
var maxFileBytes int64 = 10 << 20 // 10 MiB

// extractTree writes every file in the commit's tree to dir. Two classes are
// skipped: entries whose path escapes dir (a maliciously crafted repository —
// konflate may be pointed at a public repo it does not own — could carry a tree
// entry like "../../etc/x", and writing it via filepath.Join would land outside
// dir, a zip-slip), and files larger than maxFileBytes. go-git's blob reader
// yields blob bytes, not symlinks, so regular files are the only thing written.
//
// Both trees are extracted under the mirror read lock, so per-file work is kept
// lean: the blob streams straight to disk (no Contents() buffer→string→[]byte
// copies), and MkdirAll is skipped when an entry shares the previous one's
// directory — tree iteration is path-sorted, so same-dir files arrive in a run.
func extractTree(c *object.Commit, dir string) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	var lastDir string
	var haveDir bool
	return tree.Files().ForEach(func(f *object.File) error {
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !withinRoot(dir, dst) {
			return nil // path-traversal entry; never write outside the root
		}
		if f.Size > maxFileBytes {
			return nil // oversized blob; not review-relevant, skip to bound memory/disk
		}
		if d := filepath.Dir(dst); !haveDir || d != lastDir {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			lastDir, haveDir = d, true
		}
		return writeBlob(f, dst)
	})
}

// writeBlob streams f's blob contents into a new file at dst. Copying through
// f.Reader() avoids the two full-content copies File.Contents() makes (it reads
// the blob into a bytes.Buffer, then a string, which extractTree would copy
// again into a []byte) — wasteful per file across two whole trees under the read
// lock. The named return lets a deferred Close surface a flush error.
func writeBlob(f *object.File, dst string) (err error) {
	r, err := f.Reader()
	if err != nil {
		return err
	}
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, r)
	return err
}

// withinRoot reports whether path stays inside dir (no traversal escape).
func withinRoot(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
