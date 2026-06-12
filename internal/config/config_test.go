package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfig_InboundGating(t *testing.T) {
	t.Parallel()

	// Inbound endpoints are gated solely by their own secret — the forge token
	// is optional auth and gates nothing.
	tests := []struct {
		name      string
		token     string
		webhook   string
		push      string
		webhookOn bool
		pushOn    bool
	}{
		{"both secrets, with token", "tok", "wh", "pt", true, true},
		{"both secrets, NO token", "", "wh", "pt", true, true},
		{"no secrets, with token", "tok", "", "", false, false},
		{"only webhook secret, no token", "", "wh", "", true, false},
		{"only push token, no token", "", "", "pt", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Config{Token: tt.token, WebhookSecret: tt.webhook, PushToken: tt.push}
			if got := c.WebhookEnabled(); got != tt.webhookOn {
				t.Errorf("WebhookEnabled() = %v, want %v", got, tt.webhookOn)
			}
			if got := c.PushEnabled(); got != tt.pushOn {
				t.Errorf("PushEnabled() = %v, want %v", got, tt.pushOn)
			}
			if got := c.Authenticated(); got != (tt.token != "") {
				t.Errorf("Authenticated() = %v, want %v", got, tt.token != "")
			}
		})
	}
}

func TestWriteAccessors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                               string
		statusChecks, prComments           bool
		writeToken                         string
		appClientID, appKey                string
		wantWrite, wantSC, wantPC, wantApp bool
	}{
		{"off by default (read-only)", false, false, "", "", "", false, false, false, false},
		{"toggles on but no credential → still read-only", true, true, "", "", "", false, false, false, false},
		{"write PAT, no toggles → write only", false, false, "pat", "", "", true, false, false, false},
		{"PAT + status toggle → status on, comments off", true, false, "pat", "", "", true, true, false, false},
		{"PAT + comment toggle → comments on, status off", false, true, "pat", "", "", true, false, true, false},
		{"App key alone enables write but isn't a complete App", true, false, "", "", "-----BEGIN KEY-----", true, true, false, false},
		{"complete App (client id + key) → configured", true, true, "", "Iv1", "-----BEGIN KEY-----", true, true, true, true},
		{"App client id without key → not configured", true, false, "", "Iv1", "", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Config{
				StatusChecks: tt.statusChecks, PRComments: tt.prComments, WriteToken: tt.writeToken,
				AppClientID: tt.appClientID, AppPrivateKey: tt.appKey,
			}
			if got := c.WriteEnabled(); got != tt.wantWrite {
				t.Errorf("WriteEnabled() = %v, want %v", got, tt.wantWrite)
			}
			if got := c.StatusChecksEnabled(); got != tt.wantSC {
				t.Errorf("StatusChecksEnabled() = %v, want %v", got, tt.wantSC)
			}
			if got := c.PRCommentsEnabled(); got != tt.wantPC {
				t.Errorf("PRCommentsEnabled() = %v, want %v", got, tt.wantPC)
			}
			if got := c.AppConfigured(); got != tt.wantApp {
				t.Errorf("AppConfigured() = %v, want %v", got, tt.wantApp)
			}
		})
	}
}

func TestLoad_NoTokenIsValid(t *testing.T) {
	// Only KONFLATE_REPO is required; absence of KONFLATE_TOKEN yields a valid
	// (unauthenticated) config, not an error.
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	t.Setenv("KONFLATE_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() without a token: %v", err)
	}
	if cfg.Authenticated() {
		t.Error("expected unauthenticated with no token")
	}
	// Inbound stays off — but because no secrets are set, not because of the token.
	if cfg.WebhookEnabled() || cfg.PushEnabled() {
		t.Error("inbound endpoints must be off without their secrets")
	}
	if cfg.Forge.Kind != ForgeGitHub || cfg.Forge.RepoPath != "owner/repo" {
		t.Errorf("forge not parsed: %+v", cfg.Forge)
	}
	if cfg.RefreshInterval != 30*time.Minute {
		t.Errorf("default RefreshInterval = %v, want 30m", cfg.RefreshInterval)
	}
}

func TestLoad_DiffConcurrency(t *testing.T) {
	// Auto-derived when unset: bounded to [1,4] regardless of host CPU count.
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxDiffConcurrency < 1 || cfg.MaxDiffConcurrency > 4 {
		t.Errorf("auto MaxDiffConcurrency = %d, want within [1,4]", cfg.MaxDiffConcurrency)
	}

	// An explicit value is respected verbatim (not capped).
	t.Setenv("KONFLATE_MAX_DIFF_CONC", "9")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxDiffConcurrency != 9 {
		t.Errorf("explicit MaxDiffConcurrency = %d, want 9", cfg.MaxDiffConcurrency)
	}
}

func TestLoad_FlateTuningDefaults(t *testing.T) {
	// Unset, the flate render knobs default to flate's own CLI values so the
	// caching applies out of the box. (HelmTemplateCacheMB is the exception — it's
	// concurrency-derived; see TestLoad_HelmTemplateCacheMB.)
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"HelmRenderCacheMB", cfg.HelmRenderCacheMB, 1024},
		{"SourceRetryAttempts", cfg.SourceRetryAttempts, 3},
		{"RenderConcurrency", cfg.RenderConcurrency, 0}, // 0 ⇒ engine derives NumCPU*4
		{"MaxDiffResources", cfg.MaxDiffResources, 500},
	} {
		if c.got != c.want {
			t.Errorf("%s default = %d, want %d", c.name, c.got, c.want)
		}
	}
	if cfg.DiffTimeout != 10*time.Minute {
		t.Errorf("DiffTimeout default = %v, want 10m", cfg.DiffTimeout)
	}
	if cfg.FetchTimeout != 2*time.Minute {
		t.Errorf("FetchTimeout default = %v, want 2m", cfg.FetchTimeout)
	}
	if cfg.CacheTTL != 168*time.Hour {
		t.Errorf("CacheTTL default = %v, want 168h", cfg.CacheTTL)
	}
}

func TestLoad_RefreshInterval(t *testing.T) {
	// <=0 is preserved as the "disable periodic refresh" sentinel (read by the
	// server's refresh loop); a positive value below the floor is raised so a
	// tiny interval can't hot-loop the forge API; the default and normal values
	// pass through unchanged.
	cases := []struct {
		name, env string
		want      time.Duration
	}{
		{"unset defaults to 30m", "", 30 * time.Minute},
		{"zero disables (preserved)", "0", 0},
		{"negative disables (preserved)", "-5m", -5 * time.Minute},
		{"tiny positive is floored", "5s", minRefreshInterval},
		{"at the floor is kept", "1m", time.Minute},
		{"normal value is kept", "45m", 45 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KONFLATE_REPO", "github://owner/repo")
			if tc.env != "" {
				t.Setenv("KONFLATE_REFRESH_INTERVAL", tc.env)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.RefreshInterval != tc.want {
				t.Errorf("RefreshInterval = %v, want %v", cfg.RefreshInterval, tc.want)
			}
		})
	}
}

func TestDefaultHelmTemplateCacheMB(t *testing.T) {
	// flate builds one in-memory template cache per orchestrator and runs two per
	// render, so the budget is divided by the render concurrency to keep the
	// aggregate (~2*256 MiB) flat instead of scaling with the CPU limit; floored
	// at 32 so a high operator-set concurrency can't shrink it to nothing.
	for _, c := range []struct {
		conc, want int
	}{
		{1, 256}, {2, 128}, {4, 64}, {8, 32}, {100, 32}, {0, 256},
	} {
		if got := defaultHelmTemplateCacheMB(c.conc); got != c.want {
			t.Errorf("defaultHelmTemplateCacheMB(%d) = %d, want %d", c.conc, got, c.want)
		}
	}
}

func TestLoad_HelmTemplateCacheMB(t *testing.T) {
	t.Setenv("KONFLATE_REPO", "github://owner/repo")

	// Unset: auto — derived from the resolved render concurrency, always positive.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := defaultHelmTemplateCacheMB(cfg.MaxDiffConcurrency); cfg.HelmTemplateCacheMB != want {
		t.Errorf("auto HelmTemplateCacheMB = %d, want %d (256/concurrency)", cfg.HelmTemplateCacheMB, want)
	}

	// An explicit positive value is respected verbatim.
	t.Setenv("KONFLATE_HELM_TEMPLATE_CACHE_MB", "128")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HelmTemplateCacheMB != 128 {
		t.Errorf("explicit HelmTemplateCacheMB = %d, want 128", cfg.HelmTemplateCacheMB)
	}

	// 0 disables the cache and is preserved (not mistaken for unset).
	t.Setenv("KONFLATE_HELM_TEMPLATE_CACHE_MB", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HelmTemplateCacheMB != 0 {
		t.Errorf("explicit 0 HelmTemplateCacheMB = %d, want 0 (disabled)", cfg.HelmTemplateCacheMB)
	}
}

func TestLoad_UnsetsSecrets(t *testing.T) {
	// Secrets load into the Config, then are removed from the process
	// environment (the `,unset` tag) so a later in-process env dump can't leak
	// them. Non-secret vars are left in place.
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	t.Setenv("KONFLATE_TOKEN", "supersecret")
	t.Setenv("KONFLATE_WEBHOOK_SECRET", "wh-secret")
	t.Setenv("KONFLATE_PUSH_TOKEN", "push-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The values are available on the config…
	if cfg.Token != "supersecret" || cfg.WebhookSecret != "wh-secret" || cfg.PushToken != "push-secret" {
		t.Fatalf("secrets not loaded into config: token=%q webhook=%q push=%q",
			cfg.Token, cfg.WebhookSecret, cfg.PushToken)
	}
	// …but gone from the environment.
	for _, k := range []string{"KONFLATE_TOKEN", "KONFLATE_WEBHOOK_SECRET", "KONFLATE_PUSH_TOKEN"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s should be unset after Load, still present as %q", k, v)
		}
	}
	// A non-secret var stays set (only secrets carry `,unset`).
	if _, ok := os.LookupEnv("KONFLATE_REPO"); !ok {
		t.Error("KONFLATE_REPO should remain set after Load")
	}
}

func TestLoad_RequiresRepo(t *testing.T) {
	t.Setenv("KONFLATE_REPO", "")
	t.Setenv("KONFLATE_TOKEN", "tok")
	if _, err := Load(); err == nil {
		t.Fatal("Load() with no KONFLATE_REPO should error")
	}
}

func TestLoad_PRFilterExpr(t *testing.T) {
	t.Setenv("KONFLATE_REPO", "github://owner/repo")

	// Unset → the default filter is always compiled (cfg.PRFilter never nil), and
	// fork rendering is off by default — forks are excluded out of the box by the
	// RenderForkPRs gate, not by the filter expression.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PRFilter == nil {
		t.Fatal("PRFilter should always be compiled — the default applies when unset")
	}
	if got := cfg.PRFilter.Source(); got != DefaultPRFilter {
		t.Errorf("default filter = %q, want %q", got, DefaultPRFilter)
	}
	if cfg.RenderForkPRs {
		t.Error("RenderForkPRs must default to false (forks not rendered out of the box)")
	}

	// A custom valid expression is compiled verbatim.
	const expr = `!pr.draft && pr.baseRef == "main"`
	t.Setenv("KONFLATE_PR_FILTER_EXPR", expr)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load with valid expr: %v", err)
	}
	if cfg.PRFilter == nil || cfg.PRFilter.Source() != expr {
		t.Fatalf("custom filter not compiled as set: %+v", cfg.PRFilter)
	}

	// The fork gate parses from its own env var (default-closed, set here).
	t.Setenv("KONFLATE_RENDER_FORK_PRS", "true")
	if cfg, err := Load(); err != nil {
		t.Fatalf("Load with fork gate: %v", err)
	} else if !cfg.RenderForkPRs {
		t.Error("KONFLATE_RENDER_FORK_PRS=true should enable fork rendering")
	}

	// A malformed expression fails fast at Load (not silently at request time).
	t.Setenv("KONFLATE_PR_FILTER_EXPR", "pr.draft &&")
	if _, err := Load(); err == nil {
		t.Fatal("Load with a malformed KONFLATE_PR_FILTER_EXPR should error")
	}
}

func TestLoad_StateDir(t *testing.T) {
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	t.Setenv("KONFLATE_CACHE_DIR", "/var/cache/konflate")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The mirror and state live under a per-repo subtree (CacheDir/repos/<key>),
	// not directly under CacheDir, so a CacheDir volume shared across
	// different-repo instances can't cross-fetch or collide on state files.
	reposRoot := filepath.Join("/var/cache/konflate", "repos")
	if filepath.Dir(cfg.RepoCacheDir) != reposRoot {
		t.Errorf("RepoCacheDir = %q, want a per-repo child of %q", cfg.RepoCacheDir, reposRoot)
	}
	if want := filepath.Join(cfg.RepoCacheDir, "state"); cfg.StateDir != want {
		t.Errorf("StateDir = %q, want %q (under the repo-keyed dir)", cfg.StateDir, want)
	}
}

// TestRepoKey verifies the per-repo cache key: distinct clone URLs must map to
// distinct directory names (else two repos collide on one mirror/state dir —
// the bug this fixes), the same URL maps stably (so the cache survives
// restarts), and the name is filesystem-safe (no separators that escape the
// cache root).
func TestRepoKey(t *testing.T) {
	t.Parallel()
	const urlA = "https://github.com/owner/repo-a.git"
	const urlB = "https://github.com/owner/repo-b.git"
	a, b := repoKey(urlA), repoKey(urlB)
	if a == b {
		t.Errorf("distinct clone URLs must yield distinct keys; both = %q", a)
	}
	if a != repoKey(urlA) {
		t.Error("repoKey must be deterministic for the same clone URL")
	}
	if a == "" {
		t.Error("repoKey must not be empty")
	}
	if strings.ContainsAny(a, `/\.`) {
		t.Errorf("key %q contains path-unsafe characters", a)
	}
}

// TestLoad_PartialAppCredential pins the fail-loud check: a GitHub App write
// credential needs both halves, so a lone client id (which would otherwise leave
// write-back silently disabled) or a lone key must error at startup.
func TestLoad_PartialAppCredential(t *testing.T) {
	t.Run("client id without key errors", func(t *testing.T) {
		t.Setenv("KONFLATE_REPO", "github://owner/repo")
		t.Setenv("KONFLATE_APP_CLIENT_ID", "Iv1")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "only one is set") {
			t.Fatalf("Load with a lone client id = %v, want an 'only one is set' error", err)
		}
	})
	t.Run("key without client id errors", func(t *testing.T) {
		t.Setenv("KONFLATE_REPO", "github://owner/repo")
		t.Setenv("KONFLATE_APP_PRIVATE_KEY", "-----BEGIN KEY-----")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "only one is set") {
			t.Fatalf("Load with a lone key = %v, want an 'only one is set' error", err)
		}
	})
	t.Run("both halves is valid and configured", func(t *testing.T) {
		t.Setenv("KONFLATE_REPO", "github://owner/repo")
		t.Setenv("KONFLATE_APP_CLIENT_ID", "Iv1")
		t.Setenv("KONFLATE_APP_PRIVATE_KEY", "-----BEGIN KEY-----")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load with both halves: %v", err)
		}
		if !cfg.AppConfigured() {
			t.Error("both halves set should report AppConfigured")
		}
	})
}
