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
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
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
// so it's mapped into this private namespace rather than refs/heads. A render
// holds Mirror.mu from fetch through extract (see the struct), so another render
// can't overwrite this shared local ref — or prune the head it points at —
// between the resolve and the extract.
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
// needs. It is safe for concurrent use.
type Mirror struct {
	bareDir  string // persistent bare repo (under the cache dir)
	workRoot string // parent for the ephemeral per-diff working trees
	url      string
	token    TokenFunc // forge credential for clone/fetch, resolved per fetch (nil ⇒ anonymous)

	// fetchTimeout bounds a single fetch (and the cold clone) so a slow or hung
	// forge can't hold the lock — and thus block every other render — for longer
	// than this. <=0 leaves the fetch bounded only by the caller's ctx.
	fetchTimeout time.Duration

	// mu serializes each render's fetch AND its tree extraction as one critical
	// section — they must not be split across two lock acquisitions. A render
	// resolves its commits during the fetch; a concurrent render's repack (which
	// deletes the superseded packfiles) or corrupt-mirror rebuild (which removes
	// the bare repo) would then delete the packs those commits point at before
	// this render extracts them, surfacing as "packfile not found" mid-extract.
	// Holding mu from fetch through extract closes that window. The expensive
	// flate render runs after Trees returns, outside the lock, so renders still
	// overlap there — only the (cheap, local) git phase is serialized.
	mu sync.Mutex
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
	// Bound the fetch on its own so a slow or hung fetch holding the lock can't
	// block every other render for the full caller deadline.
	fetchCtx, cancel := m.fetchScope(ctx)
	defer cancel()

	// Resolve the credential before taking the lock: an App token mint is a network
	// call, and holding the lock across it would stall every other render. Once
	// minted it's cached, so subsequent cycles resolve without I/O.
	auth, err := m.authFor(fetchCtx)
	if err != nil {
		return nil, err
	}

	// The fetch and the tree extraction below are ONE critical section under the
	// lock: a concurrent render's repack or rebuild between them could delete the
	// packfiles these commits point at (see the Mirror struct). The expensive flate
	// render that consumes the extracted trees runs after Trees returns, outside
	// the lock, so renders still overlap there.
	m.mu.Lock()
	defer m.mu.Unlock()

	head, base, err := m.fetch(fetchCtx, headRef, baseRef, auth)
	if err != nil {
		return nil, err
	}

	// The ephemeral work dir is created under the lock too: it's a couple of
	// syscalls (no mirror I/O, so the extra hold is negligible), and creating it
	// only after a successful fetch avoids orphaning a temp dir on a failed one.
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

// fetch runs one fetch cycle and self-heals a corrupt mirror: if the fetch fails
// because the object store is damaged, it discards the bare repo, re-seeds from a
// clean clone, and retries once — go-git's incremental fetch can't repair a
// damaged store on its own, since it advertises the broken refs as haves so the
// forge never re-sends the missing objects. The caller holds the lock (so the
// RemoveAll is safe — no other render is reading the store) and has resolved auth.
// The bound on the fetch (Trees's fetchScope) caps how long this can hold the lock
// and thus head-of-line-block every other render.
func (m *Mirror) fetch(ctx context.Context, headRef, baseRef string, auth *githttp.BasicAuth) (head, base *object.Commit, err error) {
	head, base, err = m.fetchLocked(ctx, headRef, baseRef, auth)
	if err == nil || !mirrorCorrupt(err) {
		return head, base, err
	}
	slog.Default().Warn("rebuilding corrupt git mirror", "error", err, "dir", m.bareDir)
	if rmErr := os.RemoveAll(m.bareDir); rmErr != nil {
		return nil, nil, fmt.Errorf("gitclone: rebuild mirror: %w", rmErr)
	}
	return m.fetchLocked(ctx, headRef, baseRef, auth)
}

// fetchLocked performs one fetch cycle: open-or-seed the mirror, refresh the base
// and head refs, repack if the packs have piled up, and resolve both commits. The
// caller holds the lock and has resolved auth. Split from fetch so the self-heal
// path can re-run the whole cycle against a freshly re-seeded mirror.
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
// damaged — a missing object or a corrupt pack/index — and so should be
// re-cloned rather than reused. Transient (network/ctx) and expected (gone head
// ref) failures are excluded first, so a re-clone never fires on a passing blip.
//
// Damage is matched on go-git's exported sentinels via errors.Is: the sentinels
// are stable API, the messages aren't. The lone text fallback is for the repack
// ref walk, which renders ErrObjectNotFound with %v (object_walker.go) and so
// breaks the chain errors.Is needs — that flattened form is the reported wedge.
func mirrorCorrupt(err error) bool {
	if err == nil {
		return false
	}
	// Expected (gone head ref) or caller-driven (deadline/cancel): not damage.
	if errors.Is(err, ErrHeadRefGone) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// A network failure is transient, not damage — and a mid-fetch truncation can
	// surface as a malformed pack. Exclude it first so a blip never re-clones.
	if _, ok := errors.AsType[net.Error](err); ok {
		return false
	}
	if errors.Is(err, plumbing.ErrObjectNotFound) ||
		errors.Is(err, packfile.ErrMalformedPackFile) ||
		errors.Is(err, idxfile.ErrMalformedIdxFile) {
		return true
	}
	// The repack walk flattens ErrObjectNotFound with %v, so errors.Is misses it.
	return strings.Contains(err.Error(), plumbing.ErrObjectNotFound.Error())
}

// repackPackThreshold is the packfile count at which a fetch consolidates the
// mirror into a single pack. go-git appends one pack per fetch and never runs
// the equivalent of `git gc`, so without an occasional repack the pack directory
// would grow one file per render forever — slowing every object lookup and
// bloating the cache volume. 50 matches git's gc.autoPackLimit default. A var,
// not a const, so tests can lower it.
var repackPackThreshold = 50

// maybeRepack folds the mirror's packfiles into one when they have piled up past
// repackPackThreshold, reporting whether it did. repackMirror writes a single new
// pack holding every object reachable from the mirror's refs — so the unreachable
// objects a force-push leaves behind are reclaimed too — then deletes the superseded
// packs. That deletion invalidates the open handle's cached pack set, so the caller
// must re-open the mirror before reading objects through it. Caller holds the lock.
func (m *Mirror) maybeRepack(repo *git.Repository) (bool, error) {
	n, err := m.countPacks()
	if err != nil {
		return false, err
	}
	if n < repackPackThreshold {
		return false, nil
	}
	if err := repackMirror(repo); err != nil {
		return false, fmt.Errorf("gitclone: repack mirror: %w", err)
	}
	return true, nil
}

// repackMirror consolidates every packfile into one holding the objects reachable
// from the mirror's refs, then deletes the superseded packs — the `git gc` go-git
// won't run on its own.
//
// It stands in for go-git's Repository.RepackObjects, whose object walker recurses
// into gitlink (submodule) tree entries: it calls GetObject on a commit that lives
// in the submodule's own repository, never this one, so any repack of a repo that
// contains a submodule fails with "object not found". (konflate then misread that
// as a damaged store and "healed" it with a pointless re-clone, every repack,
// forever.) The walk here treats a submodule entry as a leaf, exactly as git does
// at a submodule boundary. A genuinely missing object — real damage — still surfaces
// from the walk or the encoder, and mirrorCorrupt classifies it as corruption.
func repackMirror(repo *git.Repository) error {
	pos, ok := repo.Storer.(storer.PackedObjectStorer)
	if !ok {
		return errors.New("gitclone: storer is not a PackedObjectStorer")
	}
	oldPacks, err := pos.ObjectPacks()
	if err != nil {
		return err
	}

	seen := make(map[plumbing.Hash]struct{})
	refs, err := repo.References()
	if err != nil {
		return err
	}
	defer refs.Close()
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		return walkReachable(repo.Storer, ref.Hash(), seen)
	}); err != nil {
		return err
	}

	newPack, err := writeObjectPack(repo, seen)
	if err != nil {
		return err
	}

	// Drop every pack the new one supersedes (no age floor); skip it if a no-op
	// repack happened to reproduce the same hash.
	for _, h := range oldPacks {
		if h == newPack {
			continue
		}
		if err := pos.DeleteOldObjectPackAndIndex(h, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// walkReachable records h and every object reachable from it into seen, stopping at
// gitlink (submodule) entries — their commit belongs to the submodule's repository,
// not this one. Blobs are recorded by hash without being loaded; only commits,
// trees, and tags are fetched, to follow their links.
func walkReachable(s storer.EncodedObjectStorer, h plumbing.Hash, seen map[plumbing.Hash]struct{}) error {
	if _, ok := seen[h]; ok {
		return nil
	}
	obj, err := object.GetObject(s, h)
	if err != nil {
		return fmt.Errorf("walk %s: %w", h, err)
	}
	seen[h] = struct{}{}
	switch o := obj.(type) {
	case *object.Commit:
		if err := walkReachable(s, o.TreeHash, seen); err != nil {
			return err
		}
		for _, p := range o.ParentHashes {
			if err := walkReachable(s, p, seen); err != nil {
				return err
			}
		}
	case *object.Tree:
		for i := range o.Entries {
			switch e := o.Entries[i]; e.Mode {
			case filemode.Dir:
				if err := walkReachable(s, e.Hash, seen); err != nil {
					return err
				}
			case filemode.Submodule:
				// A gitlink points at a commit in the submodule's repository, which
				// this mirror never fetches. Recursing into it (as go-git's own
				// walker does) would fail with "object not found"; skip it.
			default:
				seen[e.Hash] = struct{}{} // blob (regular/executable/symlink) — a leaf
			}
		}
	case *object.Tag:
		if err := walkReachable(s, o.Target, seen); err != nil {
			return err
		}
	}
	return nil
}

// writeObjectPack encodes objs into one new packfile in the repo's object store and
// returns its hash, mirroring go-git's createNewObjectPack (OFS deltas, the
// configured pack window). The writer must be closed before the caller deletes the
// old packs, so the encode is scoped to this function. The mirror only ever holds
// packed objects (clone and fetch both write packs, never loose), so there are no
// loose copies to reclaim afterwards.
func writeObjectPack(repo *git.Repository, objs map[plumbing.Hash]struct{}) (h plumbing.Hash, err error) {
	pfw, ok := repo.Storer.(storer.PackfileWriter)
	if !ok {
		return h, errors.New("gitclone: storer is not a PackfileWriter")
	}
	wc, err := pfw.PackfileWriter()
	if err != nil {
		return h, err
	}
	defer func() {
		if cerr := wc.Close(); err == nil {
			err = cerr
		}
	}()

	cfg, err := repo.Config()
	if err != nil {
		return h, err
	}
	hashes := make([]plumbing.Hash, 0, len(objs))
	for hash := range objs {
		hashes = append(hashes, hash)
	}
	// useRefDeltas=false → OFS deltas, matching go-git's zero RepackConfig.
	enc := packfile.NewEncoder(wc, repo.Storer, false)
	return enc.Encode(hashes, cfg.Pack.Window)
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
// by explicit fetches. Caller holds the lock.
//
// Deliberately opens a fresh *git.Repository per call rather than caching one on
// the Mirror: a repack (maybeRepack) deletes the old packfiles, so a cached
// handle's pack set would go stale, and go-git's storer also mutates
// unsynchronized object-cache maps lazily on read. A fresh handle per render
// sidesteps both.
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
