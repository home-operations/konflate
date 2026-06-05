// Package gitclone fetches a repository and materializes the two trees a PR
// diff needs: the PR head, and the merge-base of the head and the target
// branch (GitHub-style — so changes that landed on the base branch after the
// PR opened don't pollute the diff). Pure go-git; no `git` CLI shellout.
package gitclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Result holds the two extracted working trees plus a cleanup func that
// removes them. Callers must call Cleanup.
type Result struct {
	BaseDir string // merge-base(head, base) tree
	HeadDir string // head tree
	Cleanup func()
}

// Clone clones cloneURL, computes the merge-base of headRef and baseRef, and
// extracts both commit trees to temp directories. token may be empty for
// public repositories (anonymous). headRef/baseRef are branch names.
func Clone(ctx context.Context, cloneURL, token, headRef, baseRef string) (_ *Result, err error) {
	root, err := os.MkdirTemp("", "konflate-clone-")
	if err != nil {
		return nil, fmt.Errorf("gitclone: temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	var auth *githttp.BasicAuth
	if token != "" {
		// Any non-empty username works; the token is the password over HTTPS.
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}

	// Bare, single-branch clone of the base branch, then fetch only the PR head
	// branch. The two share history, so the merge-base is still computable, but
	// we avoid pulling the repo's other (often many) branches — a large saving
	// on branch-heavy repos. (Cross-fork PR heads aren't in this repo's refs and
	// were never fetched here regardless.)
	repo, err := git.PlainCloneContext(ctx, filepath.Join(root, "repo.git"), true, &git.CloneOptions{
		URL:           cloneURL,
		Auth:          auth,
		NoCheckout:    true,
		Tags:          git.NoTags,
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(baseRef),
	})
	if err != nil {
		return nil, fmt.Errorf("gitclone: clone %s (base %q): %w", cloneURL, baseRef, err)
	}
	headSpec := gitconfig.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", headRef, headRef))
	if err := repo.FetchContext(ctx, &git.FetchOptions{
		Auth:     auth,
		RefSpecs: []gitconfig.RefSpec{headSpec},
		Tags:     git.NoTags,
	}); err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, fmt.Errorf("gitclone: fetch head %q: %w", headRef, err)
	}

	headCommit, err := resolveCommit(repo, headRef)
	if err != nil {
		return nil, fmt.Errorf("gitclone: head %q: %w", headRef, err)
	}
	baseCommit, err := resolveCommit(repo, baseRef)
	if err != nil {
		return nil, fmt.Errorf("gitclone: base %q: %w", baseRef, err)
	}

	mergeBase := baseCommit
	bases, err := headCommit.MergeBase(baseCommit)
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
	if err := extractTree(mergeBase, res.BaseDir); err != nil {
		return nil, fmt.Errorf("gitclone: extract merge-base: %w", err)
	}
	if err := extractTree(headCommit, res.HeadDir); err != nil {
		return nil, fmt.Errorf("gitclone: extract head: %w", err)
	}
	return res, nil
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
