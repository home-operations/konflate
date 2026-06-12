package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/home-operations/konflate/internal/prfilter"
)

// Config holds the complete runtime configuration for konflate.
// All fields are populated from environment variables via caarlos0/env.
// Call [Load] to parse and validate; do not construct directly.
type Config struct {
	// Repo is the forge URI identifying the repository and forge instance.
	// See [ForgeURI] for the format. Examples:
	//   github://owner/repo
	//   github://ghe.example.com/owner/repo
	//   gitlab://group/subgroup/repo
	//   forgejo://git.example.com/owner/repo
	Repo string `env:"KONFLATE_REPO,required"`

	// Token is the forge API token used for API calls and cloning. Optional and
	// purely for read auth — it raises the forge API rate limit and unlocks
	// private repositories. It gates no feature (see [Config.Authenticated]).
	// Never included in any HTTP response or log line, and unset from the process
	// environment once read (defense-in-depth — a later in-process os.Environ
	// dump can't leak it; Load runs exactly once at startup). NB: this does not
	// scrub /proc/<pid>/environ or the Pod spec, only the running process's env.
	Token string `env:"KONFLATE_TOKEN,unset"`

	// ClusterPath is the directory flate renders from — the GitRepository root
	// that Flux Kustomization spec.path values resolve against. For the standard
	// layout (root-relative paths like ./kubernetes/...) leave it empty (the repo
	// root); set it to a subdirectory only if your Kustomization paths are
	// relative to that subdirectory.
	ClusterPath string `env:"KONFLATE_CLUSTER_PATH"`

	// MergeCommand is an optional Go text/template for the "copy to merge" command
	// shown on the review screen and the PR list. Empty falls back to the forge's
	// basic CLI default. The template sees {{.Number}} and {{.Repo}} only — both
	// safe to paste into a shell (attacker-controlled PR fields are not exposed).
	MergeCommand string `env:"KONFLATE_MERGE_COMMAND"`

	// WebhookSecret is the per-forge verification secret:
	//   GitHub/GHES  — HMAC-SHA256 key (X-Hub-Signature-256)
	//   GitLab       — static token   (X-Gitlab-Token)
	//   Forgejo      — HMAC-SHA256 key (X-Gitea-Signature)
	// POST /hooks is served only when this is set AND konflate is in
	// authenticated mode (see WebhookEnabled); otherwise it returns 501. Unset
	// from the process environment once read (see Token).
	WebhookSecret string `env:"KONFLATE_WEBHOOK_SECRET,unset"`

	// PushToken is the bearer token for POST /api/prs/{n}/refresh, the
	// authenticated re-render trigger for CI workflows. The endpoint is served
	// only when this is set AND konflate is in authenticated mode (see
	// PushEnabled); otherwise it returns 501. Unset from the process environment
	// once read (see Token).
	PushToken string `env:"KONFLATE_PUSH_TOKEN,unset"`

	// --- Write-back (opt-in, off by default) ---
	// konflate's HTTP surface stays read-only: no client/guest request can cause a
	// write. These only let konflate's own render loop report results back to the
	// forge, using a write credential held by the process. Off unless configured.

	// StatusChecks, when true AND a write credential is set, makes konflate report
	// a commit status on each rendered PR head linking to the konflate review (see
	// StatusChecksEnabled).
	StatusChecks bool `env:"KONFLATE_STATUS_CHECKS" envDefault:"false"`

	// PRComments, when true AND a write credential is set, makes konflate post (and
	// then update in place, keyed by a hidden marker) a PR comment carrying the
	// rendered summary on each successful render (see PRCommentsEnabled). Shares the
	// write credential with StatusChecks; the two are independent toggles.
	PRComments bool `env:"KONFLATE_PR_COMMENTS" envDefault:"false"`

	// PRCommentTemplateFile is an optional path to a Go text/template that renders
	// the PR-comment body, replacing the built-in summary. It's parsed once at
	// startup; the konflate marker is injected automatically, so the template need
	// not include it. Empty uses the built-in default. See the server's
	// commentTemplateData for the values exposed to the template.
	PRCommentTemplateFile string `env:"KONFLATE_PR_COMMENT_TEMPLATE_FILE"`

	// WriteToken is a forge token used only for write-back (commit statuses, and
	// later PR comments) — kept separate from Token so it can be scoped to just the
	// write permissions a read token shouldn't carry. Unset from the process
	// environment once read (see Token). On GitHub, prefer the App credentials
	// below; WriteToken is the universal (and only) option for Forgejo and GitLab.
	WriteToken string `env:"KONFLATE_WRITE_TOKEN,unset"`

	// GitHub App credentials — an alternative to WriteToken for GitHub only, and
	// the preferred write credential there: konflate authenticates as the App and
	// mints short-lived, narrowly-scoped installation tokens (a revocable bot
	// identity rather than a standing PAT). AppClientID is the App's client id,
	// AppPrivateKey its PEM private key (unset from the environment once read, see
	// Token). The installation is resolved automatically from the repo, so there's
	// no installation id to configure.
	AppClientID   string `env:"KONFLATE_APP_CLIENT_ID"`
	AppPrivateKey string `env:"KONFLATE_APP_PRIVATE_KEY,unset"`

	// PublicURL is konflate's externally-reachable base URL (e.g.
	// https://konflate.example.com). Write-back uses it to build the review link a
	// commit status points back to; without it the status is posted with no link.
	// The SPA derives its own URLs from the request and never needs this.
	PublicURL string `env:"KONFLATE_PUBLIC_URL"`

	// PRFilterExpr is a CEL expression deciding which PRs konflate tracks (lists,
	// renders, comments on) — the single PR filter, with no separate label
	// allowlist or fork toggle. It evaluates against one variable, pr:
	//
	//	pr.number      int       PR number
	//	pr.title       string    PR title
	//	pr.author      string    author login
	//	pr.state       string    raw forge state ("open"/"merged"/…)
	//	pr.open        bool       normalized: still an open PR
	//	pr.merged      bool       closed via merge
	//	pr.draft       bool       draft PR
	//	pr.fork        bool       head is in a different repo (external contribution)
	//	pr.headRef     string    head branch
	//	pr.headSha     string    head commit SHA
	//	pr.baseRef     string    target branch
	//	pr.url         string    PR URL
	//	pr.createdAt   timestamp opened-at time
	//	pr.labels      list      [{name, color}] — e.g. pr.labels.exists(l, l.name == "x")
	//
	// The expression must return a boolean; it is compiled and type-checked at
	// startup, so a malformed filter fails fast. Empty defaults to
	// [DefaultPRFilter] ("true") — render every open PR. It decides *which* PRs;
	// forks are gated separately by [RenderForkPRs] (default off, AND-ed in by the
	// server), so editing this expression can never accidentally enable fork
	// rendering. A PR the filter excludes is listed but hidden (greyed, never
	// rendered). Examples:
	//	one cluster only:     pr.labels.exists(l, l.name == "cluster/production")
	//	non-draft, main base: !pr.draft && pr.baseRef == "main"
	PRFilterExpr string `env:"KONFLATE_PR_FILTER_EXPR"`

	// RenderForkPRs gates rendering of fork PRs — an explicit, default-closed
	// switch AND-ed with PRFilterExpr. Rendering a fork runs untrusted external
	// code (SSRF via attacker-chosen sources, resource exhaustion), so a fork is
	// rendered only when this is true AND the filter admits it; otherwise it is
	// listed but hidden. Kept separate from the filter expression so changing the
	// expression can't silently enable forks.
	RenderForkPRs bool `env:"KONFLATE_RENDER_FORK_PRS" envDefault:"false"`

	// PRFilter is the compiled filter — PRFilterExpr, or [DefaultPRFilter] when
	// that is empty — built in [Load]. A derived field, like Forge; never set it
	// directly.
	PRFilter *prfilter.Program `env:"-"`

	// Port is the main HTTP server listen port (UI, API, /ws, /hooks).
	Port int `env:"KONFLATE_PORT" envDefault:"8080"`

	// MetricsAddr is the listen address for the separate operational server
	// that serves /metrics. Kept off the main (potentially public-facing) port
	// so operational detail is never exposed alongside the UI. Bind it to a
	// loopback address (e.g. "127.0.0.1:9090") to restrict it to the host /
	// a sidecar scraper. Health probes stay on the main port.
	MetricsAddr string `env:"KONFLATE_METRICS_ADDR" envDefault:":9090"`

	// LogLevel controls slog verbosity: debug, info, warn, or error.
	LogLevel string `env:"KONFLATE_LOG_LEVEL" envDefault:"info"`

	// LogFormat selects slog output format: "json" (default) or "text".
	LogFormat string `env:"KONFLATE_LOG_FORMAT" envDefault:"json"`

	// CacheDir is the directory for flate source caches (Helm charts, OCI
	// layers, git objects). Shared across diff jobs; persisted across restarts.
	CacheDir string `env:"KONFLATE_CACHE_DIR"`

	// RepoCacheDir is the per-repository subtree of CacheDir holding the two
	// on-disk areas tied to one specific repository: the bare git mirror and the
	// persisted diff state. It is keyed by a hash of the clone URL (see repoKey)
	// so several konflate instances tracking different repositories can share a
	// single CacheDir volume — worthwhile because flate's content-addressed source
	// cache (which stays directly under CacheDir) is safe to share — without one
	// instance silently fetching another's repo or colliding on its state files.
	// Derived in [Load]; not configurable.
	RepoCacheDir string `env:"-"`

	// StateDir is where konflate persists its rendered diffs (one zstd-compressed
	// JSON per PR) so the store survives restarts. Derived as <RepoCacheDir>/state
	// in [Load] — it rides on the same volume as the source cache, so persistence
	// is automatic once that volume is durable, and there is no separate knob. It
	// sits beside flate's cache entries, which the cache GC leaves untouched.
	StateDir string `env:"-"`

	// CacheTTL bounds how long an unused entry stays in the on-disk source cache
	// (Helm charts, OCI layers, git sources) before a periodic sweep prunes it.
	// Without it the cache only grows — every distinct source a PR ever rendered
	// stays on the (operator-mounted) volume forever. Bare git mirrors are kept
	// regardless (re-cloning them is expensive). <=0 disables the sweep (cache
	// grows unbounded; an operator's explicit choice). In Go duration form.
	CacheTTL time.Duration `env:"KONFLATE_CACHE_TTL" envDefault:"168h"` // 7d

	// CloneDir is the base directory for ephemeral per-diff PR head/base
	// clones. Cleaned up after each diff job completes.
	CloneDir string `env:"KONFLATE_CLONE_DIR"`

	// MaxDiffConcurrency is the maximum number of diff jobs that may run
	// concurrently. Higher values improve throughput but increase memory use
	// (each job holds two in-process flate orchestrators). Unset or 0 derives a
	// default from the CPU budget (GOMAXPROCS, capped at 4); see Load.
	MaxDiffConcurrency int `env:"KONFLATE_MAX_DIFF_CONC"`

	// MaxDiffResources caps how many changed resources a single diff fully
	// renders (each carries pre-highlighted unified + side-by-side rows — the
	// dominant memory and payload cost of a DiffResult). A pathological or
	// sweeping PR that touches thousands of resources is truncated to this many;
	// the impact banner still reports the true total and the UI flags the diff as
	// truncated. <=0 disables the cap. The reviewer rarely reads past a few
	// hundred resource diffs anyway.
	MaxDiffResources int `env:"KONFLATE_MAX_DIFF_RESOURCES" envDefault:"500"`

	// --- flate render tuning ---------------------------------------------
	// These map onto flate's orchestrator config; the defaults mirror flate's
	// own CLI so an embedder gets the same caching the `flate` binary does.

	// HelmTemplateCacheMB caps flate's in-memory Helm template-output cache —
	// repeat HelmReleases with identical inputs skip re-templating, the single
	// largest CPU/allocation cost of a render. In MiB. 0 disables it; a negative
	// value (the default) derives the cap from the render concurrency in Load (see
	// defaultHelmTemplateCacheMB) so this cache tracks the memory limit, not the
	// CPU count.
	HelmTemplateCacheMB int `env:"KONFLATE_HELM_TEMPLATE_CACHE_MB" envDefault:"-1"`

	// HelmRenderCacheMB caps flate's persistent on-disk Helm render cache (under
	// CacheDir). It is reused across renders, PRs, and restarts: a re-render
	// whose charts/values are unchanged short-circuits the Helm work entirely.
	// 0 disables it. In MiB.
	HelmRenderCacheMB int `env:"KONFLATE_HELM_RENDER_CACHE_MB" envDefault:"1024"`

	// SourceRetryAttempts is the total tries flate makes per source fetch
	// (Git/OCI/Bucket) before giving up, retrying only transient network
	// failures with bounded backoff. <=1 disables retry. Hardens renders
	// against forge/registry blips.
	SourceRetryAttempts int `env:"KONFLATE_SOURCE_RETRY_ATTEMPTS" envDefault:"3"`

	// RenderConcurrency caps the reconcile goroutines flate runs within a
	// single render. <=0 derives a default (runtime.NumCPU()*4) in the engine;
	// bounding it stops a fan-out PR from oversubscribing CPU/memory, especially
	// alongside MaxDiffConcurrency parallel renders.
	RenderConcurrency int `env:"KONFLATE_RENDER_CONCURRENCY"`

	// DiffTimeout bounds a single PR render end-to-end (git fetch + both flate
	// renders). Without it a pathological or hostile PR — konflate may watch a
	// repo it doesn't own — could occupy one of the few render slots forever;
	// the deadline frees the slot and surfaces the failure. <=0 disables it.
	// Generous by default so a legit cold render isn't cut short; lower it on
	// public/untrusted instances.
	DiffTimeout time.Duration `env:"KONFLATE_DIFF_TIMEOUT" envDefault:"10m"`

	// FetchTimeout bounds just the git fetch (and the first cold clone) within a
	// render, separately from DiffTimeout. The fetch runs under the persistent
	// mirror's write lock, so every other render blocks behind it — a single
	// slow or hung forge fetch otherwise holds that lock for the whole
	// DiffTimeout (10m by default) and starves all render slots at once. A
	// dedicated, much shorter bound makes a stuck fetch give up and release the
	// lock fast, so the queue keeps moving. Healthy fetches are seconds — even a
	// cold single-branch bare clone of a Flux config repo is well under this — so
	// raise it only for a very large repo on a slow link. It is also clamped by
	// whatever DiffTimeout budget remains, so it can never exceed the end-to-end
	// cap. <=0 disables it (the fetch is then bounded only by DiffTimeout).
	FetchTimeout time.Duration `env:"KONFLATE_FETCH_TIMEOUT" envDefault:"2m"`

	// RefreshInterval is how often konflate re-lists PRs (to discover newly
	// opened ones and reconcile closed ones) and, per open PR, re-renders it if
	// its last render is older than this. It's the safety net that keeps PRs
	// current even if an inbound webhook misfires; webhooks/pushes still update
	// PRs immediately and reset their per-PR clock. Merged/closed PRs are frozen
	// and never auto-refresh. <=0 disables the periodic refresh entirely (inbound
	// webhooks/pushes become the only triggers), like every sibling duration knob.
	// A positive value is floored to [minRefreshInterval] in [Load] so a tiny
	// interval can't turn the refresh into a forge-API hot loop — use webhooks for
	// near-real-time updates instead.
	RefreshInterval time.Duration `env:"KONFLATE_REFRESH_INTERVAL" envDefault:"30m"`

	// ClosedRetention bounds how long a merged PR stays on the "recently merged"
	// shelf below the open list before it is pruned. Abandoned (closed-unmerged)
	// PRs are dropped immediately regardless. The shelf is persisted (see
	// StateDir) and reloaded on restart when the cache volume is durable, so this
	// window — and the file behind it — outlives a single process. <=0 disables
	// the age cap (merged PRs kept indefinitely).
	ClosedRetention time.Duration `env:"KONFLATE_CLOSED_PR_TTL" envDefault:"336h"` // 14d

	// ClosedRetentionMax caps how many merged PRs are retained at once (the
	// most-recent win). Each retained PR holds its fully rendered diff — in
	// memory and, when persisted, on disk — so this count is what bounds both.
	// <=0 disables the count cap (age-only; with both disabled, kept forever).
	ClosedRetentionMax int `env:"KONFLATE_CLOSED_PR_MAX" envDefault:"25"`

	// Forge is the parsed forge URI. Populated by Load; not settable via env.
	Forge ForgeURI `env:"-"`
}

// Authenticated reports whether a forge token is set. The token is optional and
// purely for forge read auth — it raises the API rate limit and unlocks private
// repositories. It does not gate any feature: inbound endpoints are governed
// solely by their own secrets (see WebhookEnabled / PushEnabled), so konflate
// behaves the same with or without it.
func (c *Config) Authenticated() bool { return c.Token != "" }

// WebhookEnabled reports whether POST /hooks should be served: gated solely by a
// configured webhook secret. Without the secret the endpoint returns 501, so a
// public, secret-less instance exposes no inbound trigger surface.
func (c *Config) WebhookEnabled() bool { return c.WebhookSecret != "" }

// PushEnabled reports whether POST /api/prs/{n}/refresh should be served: gated
// solely by a configured push token.
func (c *Config) PushEnabled() bool { return c.PushToken != "" }

// WriteEnabled reports whether konflate has a write-back credential — a write
// PAT or a GitHub App key. It is the master gate for any forge write: without
// it konflate stays read-only regardless of the feature toggles.
func (c *Config) WriteEnabled() bool { return c.WriteToken != "" || c.AppPrivateKey != "" }

// StatusChecksEnabled reports whether konflate should report a commit status on
// each rendered PR head: the toggle is on and a write credential is configured.
func (c *Config) StatusChecksEnabled() bool { return c.StatusChecks && c.WriteEnabled() }

// PRCommentsEnabled reports whether konflate should post/update a PR comment with
// the rendered summary: the toggle is on and a write credential is configured.
func (c *Config) PRCommentsEnabled() bool { return c.PRComments && c.WriteEnabled() }

// AppConfigured reports whether GitHub App write credentials are set (a client id
// and a private key). The installation is auto-resolved from the repo, so it's not
// part of the credential. A partial config (one without the other) is not
// "configured": the GitHub writer reports it as an error rather than silently
// falling back. GitHub only.
func (c *Config) AppConfigured() bool {
	return c.AppClientID != "" && c.AppPrivateKey != ""
}

// DefaultPRFilter is the PR filter applied when KONFLATE_PR_FILTER_EXPR is
// empty: render every open PR. Forks are gated separately by RenderForkPRs
// (default off, AND-ed in by the server) rather than by this expression, so the
// default needn't mention them and editing the filter can't enable forks.
const DefaultPRFilter = "true"

// Load parses all KONFLATE_* environment variables, validates required fields,
// and returns a ready-to-use Config. It is the only supported way to construct
// a Config — direct struct initialization bypasses the forge URI parser and
// the XDG directory defaults.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// Compile the PR filter once, here, so a malformed expression fails at
	// startup with a clear message rather than silently dropping every PR. An
	// empty expression falls back to DefaultPRFilter, so cfg.PRFilter is always
	// set and forks are excluded unless the operator opts in.
	expr := strings.TrimSpace(cfg.PRFilterExpr)
	if expr == "" {
		expr = DefaultPRFilter
	}
	prg, err := prfilter.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("config: KONFLATE_PR_FILTER_EXPR: %w", err)
	}
	cfg.PRFilter = prg

	forge, err := ParseForgeURI(cfg.Repo)
	if err != nil {
		return nil, fmt.Errorf("config: KONFLATE_REPO: %w", err)
	}
	cfg.Forge = forge

	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(xdgCacheHome(), "konflate")
	}
	// The bare mirror and persisted state are specific to this repository, so key
	// their parent directory by the clone URL. Without this, two instances sharing
	// a CacheDir volume (to share flate's safe content-addressed source cache)
	// collide: one fetches into the other's mirror — silently rendering the wrong
	// repo — and overwrites its state files. flate's cache stays directly under
	// CacheDir and remains shared.
	cfg.RepoCacheDir = filepath.Join(cfg.CacheDir, "repos", repoKey(cfg.Forge.CloneURL()))
	cfg.StateDir = filepath.Join(cfg.RepoCacheDir, "state")
	if cfg.CloneDir == "" {
		cfg.CloneDir = filepath.Join(os.TempDir(), "konflate")
	}
	if cfg.MaxDiffConcurrency <= 0 {
		cfg.MaxDiffConcurrency = defaultDiffConcurrency()
	}
	if cfg.HelmTemplateCacheMB < 0 {
		cfg.HelmTemplateCacheMB = defaultHelmTemplateCacheMB(cfg.MaxDiffConcurrency)
	}
	// Floor a positive refresh interval so a tiny value can't hot-loop the forge
	// API (re-listing and re-rendering the whole open set every tick). <=0 is left
	// as-is: the refresh loop reads it as "disabled" (inbound triggers only).
	if cfg.RefreshInterval > 0 && cfg.RefreshInterval < minRefreshInterval {
		cfg.RefreshInterval = minRefreshInterval
	}

	return cfg, nil
}

// minRefreshInterval is the smallest positive [Config.RefreshInterval] honored;
// a smaller value is raised to it in [Load]. It bounds how often the periodic
// refresh can re-list PRs and re-render the open set, so a misconfigured tiny
// interval can't exhaust the forge's API rate limit. (To disable polling
// entirely, set the interval to 0; for near-real-time updates use webhooks.)
const minRefreshInterval = time.Minute

// defaultDiffConcurrency derives the diff-render concurrency from the CPU budget
// (GOMAXPROCS is cgroup-aware on Go 1.25+, so it tracks a container's CPU
// limit), capped at 4 so a many-CPU host doesn't run too many memory-heavy
// renders at once. Floored at 1.
func defaultDiffConcurrency() int {
	// clamp(GOMAXPROCS, 1, 4)
	return max(min(runtime.GOMAXPROCS(0), 4), 1)
}

// defaultHelmTemplateCacheMB sizes flate's in-memory Helm template cache so its
// aggregate footprint stays bounded regardless of the CPU limit. flate builds
// one such cache per orchestrator and runs two per render (base + head), so up
// to 2*concurrency are live at once; dividing a fixed budget by the concurrency
// keeps the total near 2*256 MiB instead of letting it scale with GOMAXPROCS.
// These entries are live LRU references the GC can't reclaim under GOMEMLIMIT,
// so this is the one render pool that must track the memory limit rather than
// the CPU count. flate's persistent on-disk render cache (HelmRenderCacheMB)
// still carries cross-render reuse, so a smaller in-memory L1 costs little for
// konflate's changed-only renders. concurrency<=1 keeps the original 256 MiB;
// floored so a high operator-set concurrency can't shrink it to nothing.
func defaultHelmTemplateCacheMB(concurrency int) int {
	const baseMB = 256
	return max(baseMB/max(concurrency, 1), 32)
}

// xdgCacheHome returns $XDG_CACHE_HOME if set, otherwise ~/.cache (the XDG
// default on Linux and macOS). This is where flate source caches are stored
// when KONFLATE_CACHE_DIR is unset.
func xdgCacheHome() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".cache")
	}
	return filepath.Join(home, ".cache")
}

// repoKey derives a stable, filesystem-safe directory name from a clone URL so
// the per-repository cache subtree (RepoCacheDir) is unique per repo. A hash
// rather than the raw URL keeps the name bounded and free of path separators or
// other characters that would escape the cache root; the 16-hex-char (64-bit)
// prefix is collision-resistant for the handful of repos that might ever share
// one CacheDir volume.
func repoKey(cloneURL string) string {
	sum := sha256.Sum256([]byte(cloneURL))
	return hex.EncodeToString(sum[:])[:16]
}
