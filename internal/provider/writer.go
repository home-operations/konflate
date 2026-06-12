package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/go-github/v88/github"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// Write-back is opt-in and separate from the read-only Provider: it exists only
// when an operator configures a write credential, and konflate's HTTP surface
// never triggers it (writes come from konflate's own render loop). The states
// konflate reports, mapped per forge in each SetStatus.
const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusPending = "pending"
)

// Status is one commit-status update konflate writes onto a PR's head.
type Status struct {
	State       string // StatusSuccess | StatusFailure | StatusPending
	Description string // short summary shown on the PR
	TargetURL   string // where the status links — konflate's review URL
	Context     string // the status name on the PR (e.g. "konflate")
}

// Writer reports konflate's own results back to the forge — a commit status on
// the PR head and/or a summary comment on the PR. It is deliberately separate
// from the read-only Provider and is built only when a write credential is
// configured (NewWriter returns a nil Writer otherwise), so the read path can
// never accidentally write.
type Writer interface {
	// SetStatus posts (or overwrites) konflate's commit status on the PR head.
	SetStatus(ctx context.Context, pr api.PR, st Status) error
	// UpsertComment posts body as a PR comment, or edits konflate's existing
	// comment in place — the one whose body contains marker — if there is one.
	UpsertComment(ctx context.Context, pr api.PR, marker, body string) error
	// Verify checks the credential can reach the repo, exercising the full auth
	// path (for a GitHub App, minting the installation token). It returns nil on
	// success, an ErrWriteAuthRejected-wrapped error for a permanent 401/403/404,
	// or a plain (transient) error otherwise.
	Verify(ctx context.Context) error
}

// ErrWriteAuthRejected marks a write-back credential the forge rejected in a way
// that won't fix itself — a 401/403/404 (bad token, missing permission, a wrong
// GitHub App installation, or an unreachable repo), as opposed to a transient 5xx
// or network error. The server disables write-back on it and keeps trying on
// transient failures.
var ErrWriteAuthRejected = errors.New("provider: write-back credential rejected")

// rejectedIf wraps err as ErrWriteAuthRejected for a permanent auth status
// (401/403/404); any other status (or 0/unknown) stays a plain transient error.
func rejectedIf(status int, err error) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w (HTTP %d): %w", ErrWriteAuthRejected, status, err)
	default:
		return err
	}
}

// NewWriter builds the forge Writer from the configured write credential, or a
// nil Writer when write-back is disabled (no credential — the read-only default).
func NewWriter(cfg *config.Config) (Writer, error) {
	if !cfg.WriteEnabled() {
		return nil, nil
	}
	switch cfg.Forge.Kind {
	case config.ForgeGitHub:
		return newGitHubWriter(cfg)
	case config.ForgeGitLab:
		return newGitLabWriter(cfg)
	case config.ForgeForgejo:
		return newForgejoWriter(cfg)
	default:
		return nil, fmt.Errorf("provider: write-back unsupported for forge %q", cfg.Forge.Kind)
	}
}

// strOrNil maps "" to a nil *string so an empty optional field is omitted rather
// than sent as a blank value.
func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolvedString memoizes the first successful result of resolve. A failed
// attempt is not cached, so a transient error (e.g. a forge blip while learning
// konflate's own identity) is retried on the next call rather than wedging the
// feature — the same lazy, cache-on-success pattern as repoInstallTransport.
type resolvedString struct {
	mu      sync.Mutex
	val     string
	resolve func(ctx context.Context) (string, error)
}

func (r *resolvedString) get(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.val != "" {
		return r.val, nil
	}
	v, err := r.resolve(ctx)
	if err != nil {
		return "", err
	}
	r.val = v
	return v, nil
}

// --- GitHub ---

type githubWriter struct {
	client      *github.Client
	owner, repo string
	self        *resolvedString // login of konflate's own comment author; resolved once
}

func newGitHubWriter(cfg *config.Config) (*githubWriter, error) {
	client, self, err := newGitHubWriteClient(cfg)
	if err != nil {
		return nil, err
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &githubWriter{client: client, owner: owner, repo: repo, self: &resolvedString{resolve: self}}, nil
}

// newGitHubWriteClient builds the authenticated write client. A fully-configured
// GitHub App takes precedence — it's the preferred GitHub identity, minting
// short-lived installation tokens rather than carrying a standing PAT — and a
// write PAT is the fallback (and the only option on Forgejo/GitLab). A partial
// App config is an explicit error so it can't silently mask a typo'd key.
func newGitHubWriteClient(cfg *config.Config) (*github.Client, func(context.Context) (string, error), error) {
	switch {
	case cfg.AppConfigured():
		return newGitHubAppClient(cfg)
	case cfg.WriteToken != "":
		opts := append([]github.ClientOptionsFunc{github.WithAuthToken(cfg.WriteToken)},
			githubEnterpriseOpts(cfg.Forge.Host)...)
		client, err := github.NewClient(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("github: new write client: %w", err)
		}
		// The write PAT's own user is konflate's comment-author identity.
		self := func(ctx context.Context) (string, error) {
			u, _, err := client.Users.Get(ctx, "")
			if err != nil {
				return "", fmt.Errorf("github: resolve write-token user: %w", err)
			}
			return u.GetLogin(), nil
		}
		return client, self, nil
	case cfg.AppClientID != "" || cfg.AppPrivateKey != "":
		return nil, nil, fmt.Errorf("github: App write-back needs both KONFLATE_APP_CLIENT_ID and KONFLATE_APP_PRIVATE_KEY")
	default:
		return nil, nil, fmt.Errorf("github: write-back needs KONFLATE_WRITE_TOKEN or GitHub App credentials")
	}
}

// newGitHubAppClient authenticates as a GitHub App installation, minting
// short-lived installation tokens (auto-refreshed by the transport). ghinstallation
// issues the App JWT with a numeric app id; GitHub also accepts — and now
// recommends — the App's string client id as the issuer, which is the credential
// konflate is configured with, so clientIDSigner overrides the issuer claim.
func newGitHubAppClient(cfg *config.Config) (*github.Client, func(context.Context) (string, error), error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.AppPrivateKey))
	if err != nil {
		return nil, nil, fmt.Errorf("github: parse App private key (KONFLATE_APP_PRIVATE_KEY): %w", err)
	}
	signer := clientIDSigner{
		clientID: cfg.AppClientID,
		inner:    ghinstallation.NewRSASigner(jwt.SigningMethodRS256, key),
	}
	appsTr, err := ghinstallation.NewAppsTransportWithOptions(
		http.DefaultTransport, 0, ghinstallation.WithSigner(signer))
	if err != nil {
		return nil, nil, fmt.Errorf("github: App transport: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	opts := githubEnterpriseOpts(cfg.Forge.Host)
	if host := cfg.Forge.Host; host != "" {
		// GHES: the App-JWT calls (installation lookup, token mint) go to the API base.
		appsTr.BaseURL = "https://" + host + "/api/v3"
	}
	// The installation is discovered from the repo on first use — no installation
	// id to configure (see repoInstallTransport).
	opts = append(opts, github.WithTransport(&repoInstallTransport{
		apps: appsTr, host: cfg.Forge.Host, owner: owner, repo: repo,
	}))
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("github: new App client: %w", err)
	}
	// konflate's comments are authored by the App's bot user, "<app-slug>[bot]".
	// Resolve the slug from the App itself (App-JWT auth) so comment write-back only
	// ever edits konflate's own comment, never one a PR author planted with the
	// (public) marker.
	self := func(ctx context.Context) (string, error) {
		ac, err := appJWTClient(appsTr, cfg.Forge.Host)
		if err != nil {
			return "", err
		}
		app, _, err := ac.Apps.Get(ctx, "")
		if err != nil {
			return "", fmt.Errorf("github: resolve App identity: %w", err)
		}
		return app.GetSlug() + "[bot]", nil
	}
	return client, self, nil
}

// appJWTClient builds a go-github client that authenticates as the App itself (the
// App JWT), on the same API base as the write client. Used to discover the repo
// installation and konflate's own bot identity.
func appJWTClient(appsTr *ghinstallation.AppsTransport, host string) (*github.Client, error) {
	c, err := github.NewClient(append([]github.ClientOptionsFunc{
		github.WithTransport(appsTr),
	}, githubEnterpriseOpts(host)...)...)
	if err != nil {
		return nil, fmt.Errorf("github: app client: %w", err)
	}
	return c, nil
}

// repoInstallTransport authenticates as the GitHub App and, on first request,
// resolves the App's installation for the repo (so no installation id need be
// configured — like actions/create-github-app-token), then delegates to a
// per-installation token transport. Resolution is cached on success and retried
// on failure, so a transient lookup error doesn't wedge write-back permanently;
// a permanent one (the App isn't installed on the repo → 404) surfaces on the
// request, where the startup verify classifies and disables it.
type repoInstallTransport struct {
	apps        *ghinstallation.AppsTransport
	host        string // forge host for GHES, "" for github.com
	owner, repo string

	mu  sync.Mutex
	itr *ghinstallation.Transport // the resolved per-installation transport; nil until resolved
}

func (t *repoInstallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	itr, err := t.installation(req.Context())
	if err != nil {
		return nil, err
	}
	return itr.RoundTrip(req)
}

func (t *repoInstallTransport) installation(ctx context.Context) (*ghinstallation.Transport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.itr != nil {
		return t.itr, nil
	}
	// An App-JWT client (the apps transport), on the same base as the write client.
	appClient, err := appJWTClient(t.apps, t.host)
	if err != nil {
		return nil, err
	}
	inst, _, err := appClient.Apps.GetRepositoryInstallation(ctx, t.owner, t.repo)
	if err != nil {
		return nil, fmt.Errorf("github: find App installation for %s/%s: %w", t.owner, t.repo, err)
	}
	t.itr = ghinstallation.NewFromAppsTransport(t.apps, inst.GetID())
	return t.itr, nil
}

// clientIDSigner signs the GitHub App JWT with the App's client id as the issuer.
// ghinstallation builds the claims (with the timestamps GitHub requires) and sets
// the issuer to its numeric app id; we rewrite the issuer to the client id before
// signing. The claims are freshly allocated per request, so the mutation is safe.
type clientIDSigner struct {
	clientID string
	inner    ghinstallation.Signer
}

func (s clientIDSigner) Sign(claims jwt.Claims) (string, error) {
	rc, ok := claims.(*jwt.RegisteredClaims)
	if !ok {
		return "", fmt.Errorf("github: unexpected JWT claims type %T", claims)
	}
	rc.Issuer = s.clientID
	return s.inner.Sign(rc)
}

func (w *githubWriter) SetStatus(ctx context.Context, pr api.PR, st Status) error {
	state := st.State // GitHub's states match konflate's: success / failure / pending
	_, _, err := w.client.Repositories.CreateStatus(ctx, w.owner, w.repo, pr.HeadSHA, github.RepoStatus{
		State:       &state,
		TargetURL:   strOrNil(st.TargetURL),
		Description: strOrNil(st.Description),
		Context:     strOrNil(st.Context),
	})
	if err != nil {
		return fmt.Errorf("github: set status #%d: %w", pr.Number, err)
	}
	return nil
}

func (w *githubWriter) UpsertComment(ctx context.Context, pr api.PR, marker, body string) error {
	self, err := w.self.get(ctx)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	// Find konflate's own comment (the one it authored carrying the hidden marker)
	// and edit it in place; otherwise post a new one. The marker alone isn't enough:
	// it's public (the summary API emits it too), so a PR author could plant it to
	// make konflate adopt their comment — match the author as well. ListCommentsIter
	// paginates transparently.
	for c, err := range w.client.Issues.ListCommentsIter(ctx, w.owner, w.repo, pr.Number, nil) {
		if err != nil {
			return fmt.Errorf("github: list comments #%d: %w", pr.Number, err)
		}
		if c.GetUser().GetLogin() == self && c.Body != nil && strings.Contains(*c.Body, marker) {
			_, _, err = w.client.Issues.EditComment(ctx, w.owner, w.repo, c.GetID(), &github.IssueComment{Body: &body})
			if err != nil {
				return fmt.Errorf("github: edit comment #%d: %w", pr.Number, err)
			}
			return nil
		}
	}
	if _, _, err := w.client.Issues.CreateComment(ctx, w.owner, w.repo, pr.Number, &github.IssueComment{Body: &body}); err != nil {
		return fmt.Errorf("github: create comment #%d: %w", pr.Number, err)
	}
	return nil
}

func (w *githubWriter) Verify(ctx context.Context) error {
	_, resp, err := w.client.Repositories.Get(ctx, w.owner, w.repo)
	if err == nil {
		return nil
	}
	return rejectedIf(githubStatus(resp, err), fmt.Errorf("github: verify %s/%s: %w", w.owner, w.repo, err))
}

// githubStatus extracts the HTTP status behind a write-client error: the API
// response when there is one, otherwise the App-JWT failures that surface from
// the lazy transport before the request is sent — the installation lookup (a
// github.ErrorResponse) or the token mint (a ghinstallation.HTTPError). 0 if none.
func githubStatus(resp *github.Response, err error) int {
	if resp != nil {
		return resp.StatusCode
	}
	var apiErr *github.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return apiErr.Response.StatusCode
	}
	var instErr *ghinstallation.HTTPError
	if errors.As(err, &instErr) && instErr.Response != nil {
		return instErr.Response.StatusCode
	}
	return 0
}

// --- Forgejo ---

type forgejoWriter struct {
	client      *forgejo.Client
	owner, repo string
	self        *resolvedString // username of konflate's own comment author; resolved once
}

func newForgejoWriter(cfg *config.Config) (*forgejoWriter, error) {
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("forgejo: write-back needs KONFLATE_WRITE_TOKEN")
	}
	client, err := forgejo.NewClient(forgejoBaseURL(cfg.Forge.Host),
		forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken(cfg.WriteToken))
	if err != nil {
		return nil, fmt.Errorf("forgejo: new write client: %w", err)
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	self := func(context.Context) (string, error) {
		u, _, err := client.GetMyUserInfo() // the Forgejo SDK can't take a context
		if err != nil {
			return "", fmt.Errorf("forgejo: resolve write-token user: %w", err)
		}
		return u.UserName, nil
	}
	return &forgejoWriter{client: client, owner: owner, repo: repo, self: &resolvedString{resolve: self}}, nil
}

func (w *forgejoWriter) SetStatus(_ context.Context, pr api.PR, st Status) error {
	// The Forgejo SDK can't take a context (see provider ListPRs).
	_, _, err := w.client.CreateStatus(w.owner, w.repo, pr.HeadSHA, forgejo.CreateStatusOption{
		State:       forgejo.StatusState(st.State), // success / failure / pending are valid states
		TargetURL:   st.TargetURL,
		Description: st.Description,
		Context:     st.Context,
	})
	if err != nil {
		return fmt.Errorf("forgejo: set status #%d: %w", pr.Number, err)
	}
	return nil
}

func (w *forgejoWriter) UpsertComment(ctx context.Context, pr api.PR, marker, body string) error {
	self, err := w.self.get(ctx)
	if err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	// The Forgejo SDK can't take a context (see provider ListPRs).
	idx := int64(pr.Number)
	opts := forgejo.ListIssueCommentOptions{ListOptions: forgejo.ListOptions{PageSize: 50}}
	for {
		comments, resp, err := w.client.ListIssueComments(w.owner, w.repo, idx, opts)
		if err != nil {
			return fmt.Errorf("forgejo: list comments #%d: %w", pr.Number, err)
		}
		for _, c := range comments {
			// Match konflate's own comment (author + marker), not just the public marker.
			if c.Poster != nil && c.Poster.UserName == self && strings.Contains(c.Body, marker) {
				_, _, err = w.client.EditIssueComment(w.owner, w.repo, c.ID, forgejo.EditIssueCommentOption{Body: body})
				if err != nil {
					return fmt.Errorf("forgejo: edit comment #%d: %w", pr.Number, err)
				}
				return nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if _, _, err := w.client.CreateIssueComment(w.owner, w.repo, idx, forgejo.CreateIssueCommentOption{Body: body}); err != nil {
		return fmt.Errorf("forgejo: create comment #%d: %w", pr.Number, err)
	}
	return nil
}

func (w *forgejoWriter) Verify(_ context.Context) error {
	_, resp, err := w.client.GetRepo(w.owner, w.repo)
	if err == nil {
		return nil
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	return rejectedIf(status, fmt.Errorf("forgejo: verify %s/%s: %w", w.owner, w.repo, err))
}

// --- GitLab ---

type gitlabWriter struct {
	client  *gitlab.Client
	project string
	self    *resolvedString // username of konflate's own note author; resolved once
}

func newGitLabWriter(cfg *config.Config) (*gitlabWriter, error) {
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("gitlab: write-back needs KONFLATE_WRITE_TOKEN")
	}
	client, err := gitlab.NewClient(cfg.WriteToken, gitlabHostOpts(cfg.Forge.Host)...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: new write client: %w", err)
	}
	self := func(ctx context.Context) (string, error) {
		u, _, err := client.Users.CurrentUser(gitlab.WithContext(ctx))
		if err != nil {
			return "", fmt.Errorf("gitlab: resolve write-token user: %w", err)
		}
		return u.Username, nil
	}
	return &gitlabWriter{client: client, project: cfg.Forge.RepoPath, self: &resolvedString{resolve: self}}, nil
}

func (w *gitlabWriter) SetStatus(ctx context.Context, pr api.PR, st Status) error {
	_, _, err := w.client.Commits.SetCommitStatus(w.project, pr.HeadSHA, &gitlab.SetCommitStatusOptions{
		State:       gitlabBuildState(st.State),
		Name:        strOrNil(st.Context), // GitLab shows the status under its name
		Description: strOrNil(st.Description),
		TargetURL:   strOrNil(st.TargetURL),
	}, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("gitlab: set status !%d: %w", pr.Number, err)
	}
	return nil
}

func (w *gitlabWriter) UpsertComment(ctx context.Context, pr api.PR, marker, body string) error {
	self, err := w.self.get(ctx)
	if err != nil {
		return fmt.Errorf("gitlab: %w", err)
	}
	mr := int64(pr.Number)
	opts := &gitlab.ListMergeRequestNotesOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
	for {
		notes, resp, err := w.client.Notes.ListMergeRequestNotes(w.project, mr, opts, gitlab.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("gitlab: list notes !%d: %w", pr.Number, err)
		}
		for _, n := range notes {
			// Match konflate's own note (author + marker), not just the public marker.
			if n.Author.Username == self && strings.Contains(n.Body, marker) {
				_, _, err = w.client.Notes.UpdateMergeRequestNote(w.project, mr, n.ID,
					&gitlab.UpdateMergeRequestNoteOptions{Body: &body}, gitlab.WithContext(ctx))
				if err != nil {
					return fmt.Errorf("gitlab: update note !%d: %w", pr.Number, err)
				}
				return nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if _, _, err := w.client.Notes.CreateMergeRequestNote(w.project, mr,
		&gitlab.CreateMergeRequestNoteOptions{Body: &body}, gitlab.WithContext(ctx)); err != nil {
		return fmt.Errorf("gitlab: create note !%d: %w", pr.Number, err)
	}
	return nil
}

func (w *gitlabWriter) Verify(ctx context.Context) error {
	_, resp, err := w.client.Projects.GetProject(w.project, nil, gitlab.WithContext(ctx))
	if err == nil {
		return nil
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	return rejectedIf(status, fmt.Errorf("gitlab: verify %s: %w", w.project, err))
}

// gitlabBuildState maps konflate's state to GitLab's pipeline build state
// (GitLab uses "failed", not "failure").
func gitlabBuildState(s string) gitlab.BuildStateValue {
	switch s {
	case StatusSuccess:
		return gitlab.Success
	case StatusFailure:
		return gitlab.Failed
	default:
		return gitlab.Pending
	}
}
