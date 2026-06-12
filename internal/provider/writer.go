package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// --- GitHub ---

type githubWriter struct {
	client      *github.Client
	owner, repo string
}

func newGitHubWriter(cfg *config.Config) (*githubWriter, error) {
	client, err := newGitHubWriteClient(cfg)
	if err != nil {
		return nil, err
	}
	owner, repo := ownerRepo(cfg.Forge.RepoPath)
	return &githubWriter{client: client, owner: owner, repo: repo}, nil
}

// newGitHubWriteClient builds the authenticated write client. A fully-configured
// GitHub App takes precedence — it's the preferred GitHub identity, minting
// short-lived installation tokens rather than carrying a standing PAT — and a
// write PAT is the fallback (and the only option on Forgejo/GitLab). A partial
// App config is an explicit error so it can't silently mask a typo'd key.
func newGitHubWriteClient(cfg *config.Config) (*github.Client, error) {
	switch {
	case cfg.AppConfigured():
		return newGitHubAppClient(cfg)
	case cfg.WriteToken != "":
		opts := append([]github.ClientOptionsFunc{github.WithAuthToken(cfg.WriteToken)},
			githubEnterpriseOpts(cfg.Forge.Host)...)
		client, err := github.NewClient(opts...)
		if err != nil {
			return nil, fmt.Errorf("github: new write client: %w", err)
		}
		return client, nil
	case cfg.AppClientID != "" || cfg.AppPrivateKey != "" || cfg.AppInstallationID != 0:
		return nil, fmt.Errorf("github: App write-back needs all of KONFLATE_APP_CLIENT_ID, " +
			"KONFLATE_APP_PRIVATE_KEY and KONFLATE_APP_INSTALLATION_ID")
	default:
		return nil, fmt.Errorf("github: write-back needs KONFLATE_WRITE_TOKEN or GitHub App credentials")
	}
}

// newGitHubAppClient authenticates as a GitHub App installation, minting
// short-lived installation tokens (auto-refreshed by the transport). ghinstallation
// issues the App JWT with a numeric app id; GitHub also accepts — and now
// recommends — the App's string client id as the issuer, which is the credential
// konflate is configured with, so clientIDSigner overrides the issuer claim.
func newGitHubAppClient(cfg *config.Config) (*github.Client, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.AppPrivateKey))
	if err != nil {
		return nil, fmt.Errorf("github: parse App private key (KONFLATE_APP_PRIVATE_KEY): %w", err)
	}
	signer := clientIDSigner{
		clientID: cfg.AppClientID,
		inner:    ghinstallation.NewRSASigner(jwt.SigningMethodRS256, key),
	}
	appsTr, err := ghinstallation.NewAppsTransportWithOptions(
		http.DefaultTransport, 0, ghinstallation.WithSigner(signer))
	if err != nil {
		return nil, fmt.Errorf("github: App transport: %w", err)
	}
	host := cfg.Forge.Host
	opts := githubEnterpriseOpts(host)
	if host != "" {
		// GHES: ghinstallation mints tokens against the API base; set it before
		// NewFromAppsTransport copies BaseURL off the apps transport.
		appsTr.BaseURL = "https://" + host + "/api/v3"
	}
	itr := ghinstallation.NewFromAppsTransport(appsTr, cfg.AppInstallationID)
	opts = append(opts, github.WithTransport(itr))
	client, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: new App client: %w", err)
	}
	return client, nil
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
	// Find konflate's own comment (the one carrying the hidden marker) and edit it
	// in place; otherwise post a new one. ListCommentsIter paginates transparently.
	for c, err := range w.client.Issues.ListCommentsIter(ctx, w.owner, w.repo, pr.Number, nil) {
		if err != nil {
			return fmt.Errorf("github: list comments #%d: %w", pr.Number, err)
		}
		if c.Body != nil && strings.Contains(*c.Body, marker) {
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
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	// A GitHub App mints its installation token on this first call; if that mint
	// fails the github.Response is nil and the status is on the ghinstallation error.
	var he *ghinstallation.HTTPError
	if status == 0 && errors.As(err, &he) && he.Response != nil {
		status = he.Response.StatusCode
	}
	return rejectedIf(status, fmt.Errorf("github: verify %s/%s: %w", w.owner, w.repo, err))
}

// --- Forgejo ---

type forgejoWriter struct {
	client      *forgejo.Client
	owner, repo string
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
	return &forgejoWriter{client: client, owner: owner, repo: repo}, nil
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

func (w *forgejoWriter) UpsertComment(_ context.Context, pr api.PR, marker, body string) error {
	// The Forgejo SDK can't take a context (see provider ListPRs).
	idx := int64(pr.Number)
	opts := forgejo.ListIssueCommentOptions{ListOptions: forgejo.ListOptions{PageSize: 50}}
	for {
		comments, resp, err := w.client.ListIssueComments(w.owner, w.repo, idx, opts)
		if err != nil {
			return fmt.Errorf("forgejo: list comments #%d: %w", pr.Number, err)
		}
		for _, c := range comments {
			if strings.Contains(c.Body, marker) {
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
}

func newGitLabWriter(cfg *config.Config) (*gitlabWriter, error) {
	if cfg.WriteToken == "" {
		return nil, fmt.Errorf("gitlab: write-back needs KONFLATE_WRITE_TOKEN")
	}
	client, err := gitlab.NewClient(cfg.WriteToken, gitlabHostOpts(cfg.Forge.Host)...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: new write client: %w", err)
	}
	return &gitlabWriter{client: client, project: cfg.Forge.RepoPath}, nil
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
	mr := int64(pr.Number)
	opts := &gitlab.ListMergeRequestNotesOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
	for {
		notes, resp, err := w.client.Notes.ListMergeRequestNotes(w.project, mr, opts, gitlab.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("gitlab: list notes !%d: %w", pr.Number, err)
		}
		for _, n := range notes {
			if strings.Contains(n.Body, marker) {
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
