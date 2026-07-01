package api

import "time"

// JobStatus is the lifecycle state of a PR's diff computation.
type JobStatus string

const (
	JobPending JobStatus = "pending" // queued, not yet started
	JobRunning JobStatus = "running" // a worker is rendering it
	JobReady   JobStatus = "ready"   // Diff is populated
	JobError   JobStatus = "error"   // Error is populated
)

// PRStatus is one pull request plus the state of its diff job. It is the
// element type of the GET /api/prs list that drives the UI's PR list.
type PRStatus struct {
	PR
	Status JobStatus `json:"status"`
	Error  string    `json:"error,omitempty"`
	// RefreshError is set when a re-render failed but a previously rendered diff
	// is still being shown; the UI flags it without dropping the diff.
	RefreshError string    `json:"refreshError,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// ClosedAt is set once a PR leaves the forge's open set (merged); the UI
	// groups these below the open PRs and shows "merged <ago>". Nil while open.
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// Signals is a compact summary of the rendered diff, populated once the job
	// is ready, so the PR list can show triage badges without loading each diff.
	Signals *Signals `json:"signals,omitempty"`
	// Checks is the rolled-up CI status of the PR head (the forge's red/amber/
	// green), refreshed on the poll and on status webhooks — independent of the
	// diff job. Nil when no checks were reported or the fetch failed, so the list
	// shows no indicator rather than a misleading one.
	Checks *CheckRollup `json:"checks,omitempty"`
	// MergeCommand is the rendered "copy to merge" CLI command, set only for open
	// PRs when the feature is enabled. konflate never runs it — the reviewer
	// pastes it into their own shell.
	MergeCommand string `json:"mergeCommand,omitempty"`
	// Hidden marks a PR the configured filter (KONFLATE_PR_FILTER_EXPR) excludes:
	// konflate lists it (the UI greys it and groups it under the "hidden" pill,
	// out of the default open view) but never renders its diff — so a fork's
	// untrusted code is never executed. The number/title/author still show.
	Hidden bool `json:"hidden,omitempty"`
}

// Signals is the at-a-glance review summary for one PR's rendered diff.
type Signals struct {
	Resources int  `json:"resources"` // changed/added/removed resources
	Caution   int  `json:"caution"`   // caution-tier warnings (advisory → neutral check)
	Blocking  int  `json:"blocking"`  // blocking-tier warnings (fail the check); 0 today
	Images    int  `json:"images"`    // container-image changes
	Failures  int  `json:"failures"`  // resources flate could not render
	Routine   bool `json:"routine"`   // only image/chart-version changed, nothing flagged
}

// DiffEnvelope is the GET /api/prs/{n}/diff response: the job status plus the
// rendered diff when ready (or an error message). The UI keys off Status.
type DiffEnvelope struct {
	Status JobStatus   `json:"status"`
	PR     PR          `json:"pr"`
	Diff   *DiffResult `json:"diff,omitempty"`
	Error  string      `json:"error,omitempty"`
	// RefreshError is set when the last re-render failed but Diff is still the
	// last-good render (the UI shows a "couldn't refresh" banner).
	RefreshError string `json:"refreshError,omitempty"`
	// MergeCommand is the rendered "copy to merge" CLI command, set only for open
	// PRs when the feature is enabled (see PRStatus.MergeCommand).
	MergeCommand string `json:"mergeCommand,omitempty"`
	// ReviewURL is the canonical link to this PR's review in the konflate UI
	// (e.g. https://konflate.example/#/pr/142), derived from the request. Set by
	// the summary endpoint for external consumers (a PR-comment bot links back to
	// it); the SPA never needs it. Absent elsewhere.
	ReviewURL string `json:"reviewUrl,omitempty"`
	// Hidden marks a PR the filter excludes (see PRStatus.Hidden): it isn't
	// rendered, so the UI shows an "excluded by the filter" notice rather than a
	// perpetual "rendering" state.
	Hidden bool `json:"hidden,omitempty"`
	// Digest is the store's content version for this PR (its savedDigest). It lets
	// the diff endpoint build an ETag without re-marshaling the body, and is never
	// serialized — purely a server-internal handle carried alongside the envelope.
	Digest uint64 `json:"-"`
}

// Meta is the non-secret identity of this konflate instance, served at
// GET /api/meta so the UI can show the forge and repository. It deliberately
// carries no token or secret — safe to expose even when konflate is public.
type Meta struct {
	Forge string `json:"forge"` // "github" | "gitlab" | "forgejo"
	Repo  string `json:"repo"`  // "owner/repo"
	// RepoURL is the repository's web page on its forge, so the UI can link the
	// repo name in the header.
	RepoURL string `json:"repoUrl"`
	// Version is the konflate build version (stamped via ldflags; "dev" for
	// local builds), shown in the UI footer.
	Version string `json:"version,omitempty"`
	// RefreshIntervalSeconds is how often PRs auto-refresh, so the UI can show
	// "auto-updates every Nm". konflate always auto-refreshes; there is no manual
	// refresh trigger.
	RefreshIntervalSeconds int `json:"refreshIntervalSeconds"`
	// Sync reports forge-polling health; present (with OK=false) only when the last
	// PR-list attempt failed, so the UI can show a banner instead of a misleading
	// empty list. Omitted when healthy.
	Sync *SyncStatus `json:"sync,omitempty"`
	// Features reports which optional capabilities are active, so the UI gates the
	// same features the backend has turned off (see [Features]).
	Features Features `json:"features"`
}

// Features reports which optional, instance-dependent capabilities are active.
// It rides on [Meta] so the front end gates the same features the backend does:
// forge-cost read features auto-disable when konflate is anonymous (see the
// config's Authenticated), and showing a control the backend won't feed is just
// noise. To add a gate: a bool here, populate it from a config accessor where
// Meta is built, and mirror it in the UI's Meta type.
type Features struct {
	// Checks is forge CI-status polling + display (the check pill). Off when
	// anonymous — two forge calls per PR per poll would blow the unauthenticated
	// rate limit.
	Checks bool `json:"checks"`
}

// Event is a websocket message announcing a change to a PR's diff job, so the
// UI can update that PR without polling.
type Event struct {
	Type   string    `json:"type"`             // "status", "removed", "checks", or "sync"
	Number int       `json:"number"`           // the affected PR (status/removed/checks)
	Status JobStatus `json:"status,omitempty"` // set for "status" events
	Error  string    `json:"error,omitempty"`
	// Checks is set on "checks" events — the PR head's new CI rollup (State may be
	// CheckNone, which clears the indicator). Absent on other event types.
	Checks *CheckRollup `json:"checks,omitempty"`
	// Sync is set on "sync" events — the new forge-polling health (OK=true clears
	// the UI banner, OK=false raises it). Absent on other event types.
	Sync *SyncStatus `json:"sync,omitempty"`
}

// SyncReason is the machine-readable tag on a failed SyncStatus, so the UI can
// branch (a rate limit shows a countdown + token hint; a generic error doesn't).
type SyncReason string

const (
	SyncRateLimited SyncReason = "rate_limited" // the forge API rate limit was hit; RetryAt is set
	SyncError       SyncReason = "error"        // any other failure to reach or list the forge
)

// SyncStatus reports whether konflate's last attempt to list PRs from the forge
// succeeded. Surfaced on Meta (initial load) and pushed as a "sync" Event (live),
// so the UI can show a clear "can't reach the forge / rate-limited" banner instead
// of a misleading "no pull requests" empty state.
type SyncStatus struct {
	OK      bool       `json:"ok"`                // false ⇒ the last PR-list attempt failed
	Reason  SyncReason `json:"reason,omitempty"`  // why it failed (rate_limited | error)
	Message string     `json:"message,omitempty"` // human-readable detail for the banner
	// RetryAt is when a rate limit resets (Unix seconds), set only for
	// reason=="rate_limited" so the UI can show a countdown; zero otherwise.
	RetryAt int64 `json:"retryAt,omitempty"`
}
