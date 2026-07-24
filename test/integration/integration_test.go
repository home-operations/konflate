//go:build integration

// Package integration exercises konflate against a real forge and a real Flux
// repository. It is build-tagged and env-gated: it runs only when KONFLATE_REPO
// and KONFLATE_INTEGRATION_PR are set (and an appropriate KONFLATE_TOKEN for
// private repos), so it never runs in unit CI. Run it with:
//
//	KONFLATE_REPO=github://owner/repo KONFLATE_INTEGRATION_PR=123 \
//	  go test -tags integration -timeout 10m ./test/integration/...
package integration

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/config"
	"github.com/home-operations/konflate/internal/engine"
	"github.com/home-operations/konflate/internal/provider"
)

func TestRenderRealPR(t *testing.T) {
	if os.Getenv("KONFLATE_REPO") == "" {
		t.Skip("set KONFLATE_REPO and KONFLATE_INTEGRATION_PR to run the integration test")
	}
	prNumber, err := strconv.Atoi(os.Getenv("KONFLATE_INTEGRATION_PR"))
	if err != nil || prNumber == 0 {
		t.Skip("set KONFLATE_INTEGRATION_PR to a real open PR number")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	prov, err := provider.New(cfg)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pr, err := prov.GetPR(ctx, prNumber)
	if err != nil {
		t.Fatalf("GetPR(%d): %v", prNumber, err)
	}
	t.Logf("PR #%d %q: %s -> %s", pr.Number, pr.Title, pr.HeadRef, pr.BaseRef)

	// Clone with the same forge identity as the API client above, so a private
	// KONFLATE_REPO renders (mirrors cmd/konflate).
	gitToken, err := provider.GitTokenSource(cfg)
	if err != nil {
		t.Fatalf("git credential: %v", err)
	}

	res, err := engine.New(cfg, gitToken).Diff(ctx, pr)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	t.Logf("rendered: %d resources, %d image changes, %d warnings, %d render failures",
		len(res.Resources), len(res.Images), len(res.Warnings), len(res.Failures))
	if res.PRNumber != pr.Number {
		t.Errorf("PRNumber = %d, want %d", res.PRNumber, pr.Number)
	}
	if res.ChromaCSS == "" {
		t.Error("expected a chroma stylesheet in the result")
	}
}
