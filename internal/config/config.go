package config

import (
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

	// RenderForkPRs controls whether pull requests from FORKS (cross-repo, whose
	// head branch lives in a contributor's repository) are rendered. Rendering
	// runs the PR's manifests/charts through flate, which fetches the sources they
	// declare — so a fork PR is untrusted external code with real attack surface
	// (SSRF via attacker-chosen source URLs, resource exhaustion). Off by default:
	// fork PRs are listed but shown as "not rendered (fork)" until an operator
	// opts in. Same-repo PRs (the maintainers' own branches) always render.
	RenderForkPRs bool `env:"KONFLATE_RENDER_FORK_PRS" envDefault:"false"`

	// PRLabels is an optional allowlist of pull-request labels. When set, konflate
	// only tracks (lists, renders, comments on) PRs carrying at least one of these
	// labels; PRs with none are ignored entirely, and a tracked PR that loses its
	// last matching label is dropped. Matched case-insensitively against the full
	// label name, so both a bare label ("cluster") and a namespaced one
	// ("cluster:production") work. Empty (the default) tracks every open PR.
	// Comma-separated; surrounding whitespace is trimmed.
	PRLabels []string `env:"KONFLATE_PR_LABELS" envSeparator:","`

	// PRFilterExpr is an optional CEL expression for fine-grained control over
	// which PRs konflate tracks — anything the label allowlist can't express
	// (negation, author/base rules, draft handling, combinations). It evaluates
	// against a single variable, pr, with these fields:
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
	// startup, so a malformed filter fails fast. Composes with PRLabels: when
	// both are set a PR must satisfy both. Empty (the default) adds no filter.
	// Example: pr.labels.exists(l, l.name == "cluster/production") && !pr.draft
	PRFilterExpr string `env:"KONFLATE_PR_FILTER_EXPR"`

	// PRFilter is the compiled PRFilterExpr, built in [Load] (nil when unset). A
	// derived field, like Forge — never set it directly.
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
	// largest CPU/allocation cost of a render. 0 disables it. In MiB.
	HelmTemplateCacheMB int `env:"KONFLATE_HELM_TEMPLATE_CACHE_MB" envDefault:"256"`

	// HelmRenderCacheMB caps flate's persistent on-disk Helm render cache (under
	// CacheDir). It is reused across renders, PRs, and restarts: a re-render
	// whose charts/values are unchanged short-circuits the Helm work entirely.
	// 0 disables it. In MiB.
	HelmRenderCacheMB int `env:"KONFLATE_HELM_RENDER_CACHE_MB" envDefault:"1024"`

	// StageCacheMB caps flate's persistent kustomize stage cache (under
	// CacheDir). 0 disables size-based eviction (the cache grows unbounded). In
	// MiB.
	StageCacheMB int `env:"KONFLATE_STAGE_CACHE_MB" envDefault:"2048"`

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

	// RefreshInterval is how often konflate re-lists PRs (to discover newly
	// opened ones and reconcile closed ones) and, per open PR, re-renders it if
	// its last render is older than this. It's the safety net that keeps PRs
	// current even if an inbound webhook misfires; webhooks/pushes still update
	// PRs immediately and reset their per-PR clock. Merged/closed PRs are frozen
	// and never auto-refresh.
	RefreshInterval time.Duration `env:"KONFLATE_REFRESH_INTERVAL" envDefault:"30m"`

	// ClosedRetention bounds how long a merged PR stays on the "recently merged"
	// shelf below the open list before it is pruned. Abandoned (closed-unmerged)
	// PRs are dropped immediately regardless. The store is in-memory, so this is a
	// best-effort window within a single process lifetime — a restart clears it.
	ClosedRetention time.Duration `env:"KONFLATE_CLOSED_PR_TTL" envDefault:"336h"` // 14d

	// ClosedRetentionMax caps how many merged PRs are retained at once (the
	// most-recent win). Because each retained PR holds its fully rendered diff,
	// this count — not the time window — is what actually bounds memory. <=0
	// disables the count cap (time-only).
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

// Load parses all KONFLATE_* environment variables, validates required fields,
// and returns a ready-to-use Config. It is the only supported way to construct
// a Config — direct struct initialization bypasses the forge URI parser and
// the XDG directory defaults.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// Trim whitespace and drop empties so "cluster, cluster:production" or a
	// trailing comma yields clean, exact label names to match against.
	cfg.PRLabels = normalizeList(cfg.PRLabels)

	// Compile the PR filter once, here, so a malformed expression fails at
	// startup with a clear message rather than silently dropping every PR.
	if expr := strings.TrimSpace(cfg.PRFilterExpr); expr != "" {
		prg, err := prfilter.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("config: KONFLATE_PR_FILTER_EXPR: %w", err)
		}
		cfg.PRFilter = prg
	}

	forge, err := ParseForgeURI(cfg.Repo)
	if err != nil {
		return nil, fmt.Errorf("config: KONFLATE_REPO: %w", err)
	}
	cfg.Forge = forge

	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(xdgCacheHome(), "konflate")
	}
	if cfg.CloneDir == "" {
		cfg.CloneDir = filepath.Join(os.TempDir(), "konflate")
	}
	if cfg.MaxDiffConcurrency <= 0 {
		cfg.MaxDiffConcurrency = defaultDiffConcurrency()
	}

	return cfg, nil
}

// normalizeList trims surrounding whitespace from each entry and drops empties,
// in place. Used for comma-separated env lists where spacing or a stray comma
// shouldn't produce blank or padded entries.
func normalizeList(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// defaultDiffConcurrency derives the diff-render concurrency from the CPU budget
// (GOMAXPROCS is cgroup-aware on Go 1.25+, so it tracks a container's CPU
// limit), capped at 4 so a many-CPU host doesn't run too many memory-heavy
// renders at once. Floored at 1.
func defaultDiffConcurrency() int {
	// clamp(GOMAXPROCS, 1, 4)
	return max(min(runtime.GOMAXPROCS(0), 4), 1)
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
