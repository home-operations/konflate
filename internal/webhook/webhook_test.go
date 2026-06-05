package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"github.com/home-operations/konflate/internal/config"
)

// canonicalBody is the payload every test signs; pinned by TestKnownVector.
const canonicalBody = "Hello, World!"

func hmacSHA256Hex(secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonicalBody))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestKnownVector pins the crypto against GitHub's published example
// (docs: "Validating webhook deliveries"). If this matches, the computed
// signatures used in TestVerify are trustworthy — and it proves we're doing
// HMAC-SHA256 with lowercase hex, not some other hash/encoding.
func TestKnownVector(t *testing.T) {
	t.Parallel()
	const (
		secret = "It's a Secret to Everybody"
		want   = "757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	)
	if got := hmacSHA256Hex(secret); got != want {
		t.Fatalf("HMAC-SHA256 hex = %s, want %s (GitHub canonical vector)", got, want)
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	const secret = "It's a Secret to Everybody"
	sig := hmacSHA256Hex(secret) // trusted via TestKnownVector
	bodyBytes := []byte(canonicalBody)

	tests := []struct {
		name    string
		forge   config.ForgeKind
		secret  string
		header  http.Header
		body    []byte
		wantErr error // nil = must accept; otherwise errors.Is target
	}{
		// ── GitHub: X-Hub-Signature-256 = "sha256=" + hex(HMAC) ──
		{
			name:  "github valid signature",
			forge: config.ForgeGitHub, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Hub-Signature-256": {"sha256=" + sig}},
			wantErr: nil,
		},
		{
			name:  "github wrong signature is rejected",
			forge: config.ForgeGitHub, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Hub-Signature-256": {"sha256=" + hmacSHA256Hex("wrong-secret")}},
			wantErr: ErrSignatureMismatch,
		},
		{
			name:  "github missing the sha256= prefix is rejected",
			forge: config.ForgeGitHub, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Hub-Signature-256": {sig}}, // bare hex, no prefix
			wantErr: ErrSignatureMismatch,
		},
		{
			name:  "github missing header is rejected",
			forge: config.ForgeGitHub, secret: secret, body: bodyBytes,
			header:  http.Header{},
			wantErr: ErrMissingSignature,
		},
		{
			name:  "github tampered body is rejected (signature no longer matches)",
			forge: config.ForgeGitHub, secret: secret, body: []byte("Goodbye, World!"),
			header:  http.Header{"X-Hub-Signature-256": {"sha256=" + sig}},
			wantErr: ErrSignatureMismatch,
		},

		// ── Forgejo/Gitea: X-Gitea-Signature = hex(HMAC), no prefix ──
		{
			name:  "forgejo valid signature (no prefix)",
			forge: config.ForgeForgejo, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Gitea-Signature": {sig}},
			wantErr: nil,
		},
		{
			name:  "forgejo wrong signature is rejected",
			forge: config.ForgeForgejo, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Gitea-Signature": {hmacSHA256Hex("nope")}},
			wantErr: ErrSignatureMismatch,
		},

		// ── GitLab: X-Gitlab-Token = the secret verbatim ──
		{
			name:  "gitlab valid token",
			forge: config.ForgeGitLab, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Gitlab-Token": {secret}},
			wantErr: nil,
		},
		{
			name:  "gitlab wrong token is rejected",
			forge: config.ForgeGitLab, secret: secret, body: bodyBytes,
			header:  http.Header{"X-Gitlab-Token": {"not-the-secret"}},
			wantErr: ErrSignatureMismatch,
		},
		{
			name:  "gitlab missing token is rejected",
			forge: config.ForgeGitLab, secret: secret, body: bodyBytes,
			header:  http.Header{},
			wantErr: ErrMissingSignature,
		},

		// ── Defensive: never verify without a secret ──
		{
			name:  "empty secret never verifies",
			forge: config.ForgeGitHub, secret: "", body: bodyBytes,
			header:  http.Header{"X-Hub-Signature-256": {"sha256=" + sig}},
			wantErr: ErrNoSecret,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Verify(tt.forge, tt.secret, tt.header, tt.body)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify() = %v, want nil (must accept a valid request)", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify() = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}
