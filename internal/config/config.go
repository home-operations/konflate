package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/caarlos0/env/v11"
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
	// Never included in any HTTP response or log line.
	Token string `env:"KONFLATE_TOKEN"`

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
	// authenticated mode (see WebhookEnabled); otherwise it returns 501.
	WebhookSecret string `env:"KONFLATE_WEBHOOK_SECRET"`

	// PushToken is the bearer token for POST /api/prs/{n}/refresh, the
	// authenticated re-render trigger for CI workflows. The endpoint is served
	// only when this is set AND konflate is in authenticated mode (see
	// PushEnabled); otherwise it returns 501.
	PushToken string `env:"KONFLATE_PUSH_TOKEN"`

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

	// CloneDir is the base directory for ephemeral per-diff PR head/base
	// clones. Cleaned up after each diff job completes.
	CloneDir string `env:"KONFLATE_CLONE_DIR"`

	// MaxDiffConcurrency is the maximum number of diff jobs that may run
	// concurrently. Higher values improve throughput but increase memory use
	// (each job holds two in-process flate orchestrators). Unset or 0 derives a
	// default from the CPU budget (GOMAXPROCS, capped at 4); see Load.
	MaxDiffConcurrency int `env:"KONFLATE_MAX_DIFF_CONC"`

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
