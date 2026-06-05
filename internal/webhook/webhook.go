// Package webhook verifies inbound forge webhook requests. Verification uses
// the Go standard library (crypto/hmac for GitHub/Forgejo, subtle for GitLab),
// not the forge SDKs: GitLab's scheme is a plain token compare with no SDK
// helper, Forgejo/Gitea exposes none either, and crypto/hmac is the canonical,
// auditable primitive — so pulling three SDKs in solely for this isn't worth it.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/home-operations/konflate/internal/config"
)

// Verification outcomes. Callers map any non-nil error to HTTP 401.
var (
	ErrNoSecret          = errorString("webhook: no secret configured")
	ErrMissingSignature  = errorString("webhook: missing signature header")
	ErrSignatureMismatch = errorString("webhook: signature mismatch")
)

type errorString string

func (e errorString) Error() string { return string(e) }

// Verify reports whether an inbound webhook request is authentic for forge,
// given the configured secret, the request headers, and the raw body. It
// returns nil when authentic; otherwise one of the sentinel errors above.
func Verify(forge config.ForgeKind, secret string, header http.Header, body []byte) error {
	if secret == "" {
		return ErrNoSecret
	}
	switch forge {
	case config.ForgeGitHub:
		// X-Hub-Signature-256: "sha256=" + hex(HMAC-SHA256(body, secret)).
		return verifyHMAC(header.Get("X-Hub-Signature-256"), "sha256=", secret, body)
	case config.ForgeForgejo:
		// X-Gitea-Signature: hex(HMAC-SHA256(body, secret)), no prefix.
		return verifyHMAC(header.Get("X-Gitea-Signature"), "", secret, body)
	case config.ForgeGitLab:
		// X-Gitlab-Token: the shared secret verbatim (no crypto).
		return verifyToken(header.Get("X-Gitlab-Token"), secret)
	default:
		return fmt.Errorf("webhook: unsupported forge %q", forge)
	}
}

// verifyHMAC checks an HMAC-SHA256 signature header. prefix is stripped first
// (e.g. "sha256="); an empty prefix means the header is the bare hex digest.
// The comparison is constant-time (hmac.Equal).
func verifyHMAC(provided, prefix, secret string, body []byte) error {
	if provided == "" {
		return ErrMissingSignature
	}
	if prefix != "" {
		rest, ok := strings.CutPrefix(provided, prefix)
		if !ok {
			return ErrSignatureMismatch
		}
		provided = rest
	}
	got, err := hex.DecodeString(provided)
	if err != nil {
		return ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrSignatureMismatch
	}
	return nil
}

// verifyToken checks a shared-secret token header with a constant-time compare.
func verifyToken(provided, secret string) error {
	if provided == "" {
		return ErrMissingSignature
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		return ErrSignatureMismatch
	}
	return nil
}
