package config

import (
	"testing"
)

func TestParseForgeURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    ForgeURI
		wantErr string // non-empty = expect error containing this substring
	}{
		// ── GitHub cloud ──────────────────────────────────────────────────
		{
			name:  "github cloud",
			input: "github://owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "",
				RepoPath: "owner/repo",
				APIBase:  "https://api.github.com",
				WebBase:  "https://github.com",
			},
		},
		{
			name:  "github cloud org with hyphen",
			input: "github://my-org/my-repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "",
				RepoPath: "my-org/my-repo",
				APIBase:  "https://api.github.com",
				WebBase:  "https://github.com",
			},
		},
		{
			// An explicit cloud host must normalize to the cloud API base, not be
			// treated as a GHES instance (github.com/api/v3, which 404s).
			name:  "github explicit cloud host normalizes to the cloud API",
			input: "github://github.com/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "",
				RepoPath: "owner/repo",
				APIBase:  "https://api.github.com",
				WebBase:  "https://github.com",
			},
		},
		{
			name:  "gitlab explicit cloud host normalizes",
			input: "gitlab://gitlab.com/group/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "",
				RepoPath: "group/repo",
				APIBase:  "https://gitlab.com",
				WebBase:  "https://gitlab.com",
			},
		},
		{
			name:  "forgejo explicit cloud host normalizes",
			input: "forgejo://codeberg.org/owner/repo",
			want: ForgeURI{
				Kind:     ForgeForgejo,
				Host:     "",
				RepoPath: "owner/repo",
				APIBase:  "https://codeberg.org",
				WebBase:  "https://codeberg.org",
			},
		},

		// ── GitHub Enterprise Server ──────────────────────────────────────
		{
			name:  "github enterprise server",
			input: "github://ghe.example.com/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "ghe.example.com",
				RepoPath: "owner/repo",
				APIBase:  "https://ghe.example.com/api/v3",
				WebBase:  "https://ghe.example.com",
			},
		},
		{
			name:  "github enterprise server with port",
			input: "github://ghe.example.com:8080/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "ghe.example.com:8080",
				RepoPath: "owner/repo",
				APIBase:  "https://ghe.example.com:8080/api/v3",
				WebBase:  "https://ghe.example.com:8080",
			},
		},
		{
			name:  "github on localhost with port",
			input: "github://localhost:3000/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "localhost:3000",
				RepoPath: "owner/repo",
				APIBase:  "https://localhost:3000/api/v3",
				WebBase:  "https://localhost:3000",
			},
		},
		{
			name:  "github on localhost without port",
			input: "github://localhost/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "localhost",
				RepoPath: "owner/repo",
				APIBase:  "https://localhost/api/v3",
				WebBase:  "https://localhost",
			},
		},
		{
			name:  "github on bare IP address",
			input: "github://192.168.1.100/owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "192.168.1.100",
				RepoPath: "owner/repo",
				APIBase:  "https://192.168.1.100/api/v3",
				WebBase:  "https://192.168.1.100",
			},
		},

		// ── GitLab cloud ─────────────────────────────────────────────────
		{
			name:  "gitlab cloud",
			input: "gitlab://group/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "",
				RepoPath: "group/repo",
				APIBase:  "https://gitlab.com",
				WebBase:  "https://gitlab.com",
			},
		},
		{
			name:  "gitlab cloud nested subgroup",
			input: "gitlab://group/subgroup/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "",
				RepoPath: "group/subgroup/repo",
				APIBase:  "https://gitlab.com",
				WebBase:  "https://gitlab.com",
			},
		},
		{
			name:  "gitlab cloud deeply nested subgroup",
			input: "gitlab://group/sub/sub2/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "",
				RepoPath: "group/sub/sub2/repo",
				APIBase:  "https://gitlab.com",
				WebBase:  "https://gitlab.com",
			},
		},

		// ── GitLab self-hosted ────────────────────────────────────────────
		{
			name:  "gitlab self-hosted",
			input: "gitlab://gl.example.com/group/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "gl.example.com",
				RepoPath: "group/repo",
				APIBase:  "https://gl.example.com",
				WebBase:  "https://gl.example.com",
			},
		},
		{
			name:  "gitlab self-hosted nested subgroup",
			input: "gitlab://gl.example.com/group/subgroup/repo",
			want: ForgeURI{
				Kind:     ForgeGitLab,
				Host:     "gl.example.com",
				RepoPath: "group/subgroup/repo",
				APIBase:  "https://gl.example.com",
				WebBase:  "https://gl.example.com",
			},
		},

		// ── Forgejo cloud ────────────────────────────────────────────────
		{
			name:  "forgejo cloud (codeberg.org)",
			input: "forgejo://owner/repo",
			want: ForgeURI{
				Kind:     ForgeForgejo,
				Host:     "",
				RepoPath: "owner/repo",
				APIBase:  "https://codeberg.org",
				WebBase:  "https://codeberg.org",
			},
		},

		// ── Forgejo self-hosted ───────────────────────────────────────────
		{
			name:  "forgejo self-hosted",
			input: "forgejo://git.example.com/owner/repo",
			want: ForgeURI{
				Kind:     ForgeForgejo,
				Host:     "git.example.com",
				RepoPath: "owner/repo",
				APIBase:  "https://git.example.com",
				WebBase:  "https://git.example.com",
			},
		},
		{
			name:  "forgejo self-hosted with port",
			input: "forgejo://git.example.com:3000/owner/repo",
			want: ForgeURI{
				Kind:     ForgeForgejo,
				Host:     "git.example.com:3000",
				RepoPath: "owner/repo",
				APIBase:  "https://git.example.com:3000",
				WebBase:  "https://git.example.com:3000",
			},
		},

		// ── Case insensitivity ────────────────────────────────────────────
		{
			name:  "scheme uppercase is normalised",
			input: "GitHub://owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				Host:     "",
				RepoPath: "owner/repo",
				APIBase:  "https://api.github.com",
				WebBase:  "https://github.com",
			},
		},

		// ── CloneURL helper ───────────────────────────────────────────────
		{
			name:  "clone URL cloud",
			input: "github://owner/repo",
			want: ForgeURI{
				Kind:     ForgeGitHub,
				RepoPath: "owner/repo",
				APIBase:  "https://api.github.com",
				WebBase:  "https://github.com",
			},
		},

		// ── Error cases ───────────────────────────────────────────────────
		{
			name:    "empty string",
			input:   "",
			wantErr: "empty value",
		},
		{
			name:    "unknown scheme",
			input:   "bitbucket://owner/repo",
			wantErr: "unknown scheme",
		},
		{
			name:    "missing repo path — scheme only",
			input:   "github://",
			wantErr: "missing repository path",
		},
		{
			name:    "missing slash in repo path (owner only, no repo)",
			input:   "github://owner",
			wantErr: "must be at least owner/repo",
		},
		{
			name:    "missing slash in repo path (self-hosted, owner only)",
			input:   "github://ghe.example.com/owner",
			wantErr: "must be at least owner/repo",
		},
		{
			name:    "plain HTTPS URL rejected (not a forge URI)",
			input:   "https://github.com/owner/repo",
			wantErr: "unknown scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseForgeURI(tt.input)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseForgeURI(%q) = %+v, want error containing %q", tt.input, got, tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseForgeURI(%q) error = %q, want it to contain %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseForgeURI(%q) unexpected error: %v", tt.input, err)
			}

			if got.Kind != tt.want.Kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.want.Kind)
			}
			if got.Host != tt.want.Host {
				t.Errorf("Host = %q, want %q", got.Host, tt.want.Host)
			}
			if got.RepoPath != tt.want.RepoPath {
				t.Errorf("RepoPath = %q, want %q", got.RepoPath, tt.want.RepoPath)
			}
			if got.APIBase != tt.want.APIBase {
				t.Errorf("APIBase = %q, want %q", got.APIBase, tt.want.APIBase)
			}
			if got.WebBase != tt.want.WebBase {
				t.Errorf("WebBase = %q, want %q", got.WebBase, tt.want.WebBase)
			}
		})
	}
}

func TestForgeURI_CloneURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"github://owner/repo", "https://github.com/owner/repo"},
		{"github://ghe.example.com/owner/repo", "https://ghe.example.com/owner/repo"},
		{"gitlab://group/subgroup/repo", "https://gitlab.com/group/subgroup/repo"},
		{"gitlab://gl.example.com/group/repo", "https://gl.example.com/group/repo"},
		{"forgejo://owner/repo", "https://codeberg.org/owner/repo"},
		{"forgejo://git.example.com/owner/repo", "https://git.example.com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			f, err := ParseForgeURI(tt.input)
			if err != nil {
				t.Fatalf("ParseForgeURI(%q): %v", tt.input, err)
			}
			if got := f.CloneURL(); got != tt.want {
				t.Errorf("CloneURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLooksLikeHost(t *testing.T) {
	t.Parallel()

	yes := []string{
		"github.com",
		"ghe.example.com",
		"192.168.1.100",
		"localhost:3000",
		"ghe.example.com:8080",
		"localhost",
	}
	no := []string{
		"",
		"owner",
		"group",
		"myorg",
		"my-org",
	}

	for _, s := range yes {
		if !looksLikeHost(s) {
			t.Errorf("looksLikeHost(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if looksLikeHost(s) {
			t.Errorf("looksLikeHost(%q) = true, want false", s)
		}
	}
}

// contains is strings.Contains but avoids importing strings for one helper.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := range len(s) - len(sub) + 1 {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
