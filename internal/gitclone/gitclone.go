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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ErrHeadRefGone reports that the PR's head ref no longer exists on the remote —
// typically the PR was merged or closed and its branch deleted between when a
// render was enqueued and when it ran. Callers treat this as "the PR left the
// open set", not a render failure.
var ErrHeadRefGone = errors.New("gitclone: head ref no longer exists on remote")

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
	auth     *githttp.BasicAuth

	// fetch takes the write lock; merge-base + tree extraction take the read
	// lock, so several renders extract concurrently but none does so while a
	// fetch is mutating the shared object store.
	mu sync.RWMutex
}

// NewMirror builds a Mirror for cloneURL. The bare repo lives under cacheDir
// (persistent), per-diff working trees under cloneDir (ephemeral). token may be
// empty for public repositories (anonymous).
func NewMirror(cacheDir, cloneDir, cloneURL, token string) *Mirror {
	var auth *githttp.BasicAuth
	if token != "" {
		// Any non-empty username works; the token is the password over HTTPS.
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}
	return &Mirror{
		bareDir:  filepath.Join(cacheDir, "git", "mirror.git"),
		workRoot: cloneDir,
		url:      cloneURL,
		auth:     auth,
	}
}

// Trees fetches headRef and baseRef into the mirror, computes the merge-base of
// the two (GitHub-style), and extracts the head and merge-base trees to a fresh
// working directory. headRef/baseRef are branch names. Callers must call
// Result.Cleanup.
func (m *Mirror) Trees(ctx context.Context, headRef, baseRef string) (_ *Result, err error) {
	head, base, err := m.fetch(ctx, headRef, baseRef)
	if err != nil {
		return nil, err
	}

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

// fetch ensures the bare mirror exists (cloning it once), fetches the base and
// head refs into it, and resolves both to commits — all under the write lock so
// only one render mutates the store at a time. The returned commits carry their
// own storer reference, so they stay readable after the lock is released.
func (m *Mirror) fetch(ctx context.Context, headRef, baseRef string) (head, base *object.Commit, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	repo, err := m.openOrClone(ctx, baseRef)
	if err != nil {
		return nil, nil, err
	}

	// Refresh the base branch (a no-op right after the seeding clone). A missing
	// base is a real error — the PR targets it.
	if err := fetchRef(ctx, repo, m.auth, baseRef); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, nil, fmt.Errorf("gitclone: fetch base %q: %w", baseRef, err)
	}
	// A missing head ref means the branch was deleted (merged/closed PR) — report
	// it as gone so the caller reconciles the PR rather than failing the render.
	if err := fetchRef(ctx, repo, m.auth, headRef); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if errors.Is(err, git.NoMatchingRefSpecError{}) {
			return nil, nil, fmt.Errorf("gitclone: fetch head %q: %w", headRef, ErrHeadRefGone)
		}
		return nil, nil, fmt.Errorf("gitclone: fetch head %q: %w", headRef, err)
	}

	head, err = resolveCommit(repo, headRef)
	if err != nil {
		return nil, nil, fmt.Errorf("gitclone: head %q: %w", headRef, err)
	}
	base, err = resolveCommit(repo, baseRef)
	if err != nil {
		return nil, nil, fmt.Errorf("gitclone: base %q: %w", baseRef, err)
	}
	return head, base, nil
}

// openOrClone opens the bare mirror, seeding it with a single-branch bare clone
// of the base branch on first use. A bare, single-branch clone avoids pulling
// the repo's other (often many) branches; subsequent head/base refs are added
// by explicit fetches. Caller holds the write lock.
func (m *Mirror) openOrClone(ctx context.Context, baseRef string) (*git.Repository, error) {
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
		Auth:          m.auth,
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

// fetchRef force-fetches a single branch into the mirror's refs/heads, pulling
// only that branch (no tags). An explicit refspec lets the mirror accumulate
// any base/head branch a PR needs, regardless of the branch it was seeded with.
func fetchRef(ctx context.Context, repo *git.Repository, auth *githttp.BasicAuth, ref string) error {
	spec := gitconfig.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", ref, ref))
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

// extractTree writes every file in the commit's tree to dir. Entries whose
// path escapes dir are skipped: a maliciously crafted repository (konflate may
// be pointed at a public repo it does not own) could carry a tree entry like
// "../../etc/x", and writing it via filepath.Join would land outside dir
// (a zip-slip). go-git's File.Contents yields blob bytes, not symlinks, so
// regular files are the only thing written.
func extractTree(c *object.Commit, dir string) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	return tree.Files().ForEach(func(f *object.File) error {
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !withinRoot(dir, dst) {
			return nil // path-traversal entry; never write outside the root
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		contents, err := f.Contents()
		if err != nil {
			return err
		}
		return os.WriteFile(dst, []byte(contents), 0o644)
	})
}

// withinRoot reports whether path stays inside dir (no traversal escape).
func withinRoot(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
