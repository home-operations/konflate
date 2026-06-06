package config

import (
	"os"
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
	// caching applies out of the box.
	t.Setenv("KONFLATE_REPO", "github://owner/repo")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"HelmTemplateCacheMB", cfg.HelmTemplateCacheMB, 256},
		{"HelmRenderCacheMB", cfg.HelmRenderCacheMB, 1024},
		{"StageCacheMB", cfg.StageCacheMB, 2048},
		{"SourceRetryAttempts", cfg.SourceRetryAttempts, 3},
		{"RenderConcurrency", cfg.RenderConcurrency, 0}, // 0 ⇒ engine derives NumCPU*4
	} {
		if c.got != c.want {
			t.Errorf("%s default = %d, want %d", c.name, c.got, c.want)
		}
	}
	if cfg.DiffTimeout != 10*time.Minute {
		t.Errorf("DiffTimeout default = %v, want 10m", cfg.DiffTimeout)
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
