package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/konflate/internal/api"
)

// fakeChecker resolves refs per its maps: errRefs → indeterminate, missing →
// absent, everything else present. It records every ref dialed (concurrency-safe).
type fakeChecker struct {
	missing map[string]bool
	errRefs map[string]bool

	mu    sync.Mutex
	calls []string
}

func (f *fakeChecker) Exists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	f.mu.Unlock()
	if f.errRefs[ref] {
		return false, errors.New("indeterminate")
	}
	return !f.missing[ref], nil
}

func TestVerifyImages(t *testing.T) {
	t.Parallel()
	chk := &fakeChecker{
		missing: map[string]bool{"ghcr.io/app:9.9.9": true},
		errRefs: map[string]bool{"private.example.com/x:1.0": true},
	}
	images := []api.ImageChange{
		{Name: "ghcr.io/app", To: "9.9.9"},             // absent → caution
		{Name: "ghcr.io/ok", To: "1.2.3"},              // present → nothing
		{Name: "private.example.com/x", To: "1.0"},     // indeterminate → skipped, never flagged
		{Name: "ghcr.io/removed", From: "1.0", To: ""}, // a removal → not checked
	}
	got := verifyImages(t.Context(), chk, images, 0, discardLog())

	if len(got) != 1 {
		t.Fatalf("want exactly 1 image-not-found caution, got %d: %+v", len(got), got)
	}
	if w := got[0]; w.Rule != "image-not-found" || w.Level != api.LevelCaution || w.Resource != "ghcr.io/app" {
		t.Errorf("unexpected warning: %+v", w)
	}
	for _, ref := range chk.calls {
		if ref == "ghcr.io/removed:" || ref == "ghcr.io/removed" {
			t.Errorf("a removed image must not be dialed, got %q", ref)
		}
	}
}

func TestImageRef(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, ver, want string }{
		{"ghcr.io/x", "1.2.3", "ghcr.io/x:1.2.3"},
		{"ghcr.io/x", "sha256:abc", "ghcr.io/x@sha256:abc"},
		{"ghcr.io/x", "1.2.3@sha256:abc", "ghcr.io/x:1.2.3@sha256:abc"},
		{"", "1.0", ""},
		{"ghcr.io/x", "", ""},
	}
	for _, tc := range cases {
		if got := imageRef(tc.name, tc.ver); got != tc.want {
			t.Errorf("imageRef(%q, %q) = %q, want %q", tc.name, tc.ver, got, tc.want)
		}
	}
}

func TestRenderFuncSkipsForkPRs(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{PRNumber: pr.Number, Images: []api.ImageChange{{Name: "ghcr.io/app", To: "9.9.9"}}}, nil
	}}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{}, eng)
	chk := &fakeChecker{missing: map[string]bool{"ghcr.io/app:9.9.9": true}}
	s.imageCheck = chk
	render := s.renderFunc()

	// Trusted (non-fork) PR: the image is verified and the missing one flagged.
	res, err := render(t.Context(), api.PR{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := countRule(res.Warnings, "image-not-found"); n != 1 {
		t.Errorf("trusted PR: want 1 image-not-found caution, got %d", n)
	}

	// Fork PR: never dialed, never flagged — a fork's images are attacker-chosen (SSRF).
	before := len(chk.calls)
	res, err = render(t.Context(), api.PR{Number: 2, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := countRule(res.Warnings, "image-not-found"); n != 0 {
		t.Errorf("fork PR: want 0 image-not-found cautions, got %d", n)
	}
	if len(chk.calls) != before {
		t.Errorf("fork PR must not dial the registry; extra calls: %v", chk.calls[before:])
	}
}

func countRule(ws []api.Warning, rule string) int {
	n := 0
	for _, w := range ws {
		if w.Rule == rule {
			n++
		}
	}
	return n
}

// checkerFunc adapts a function to the imageChecker interface.
type checkerFunc func(context.Context, string) (bool, error)

func (f checkerFunc) Exists(ctx context.Context, ref string) (bool, error) { return f(ctx, ref) }

func TestVerifyImagesAppliesPerDialTimeout(t *testing.T) {
	t.Parallel()
	images := []api.ImageChange{{Name: "ghcr.io/x", To: "1.0"}}

	// A positive timeout gives each dial its own deadline — the per-dial DoS bound.
	var sawDeadline bool
	chk := checkerFunc(func(ctx context.Context, _ string) (bool, error) {
		_, sawDeadline = ctx.Deadline()
		return true, nil
	})
	verifyImages(t.Context(), chk, images, 50*time.Millisecond, discardLog())
	if !sawDeadline {
		t.Error("a positive timeout must give each registry dial a context deadline")
	}

	// timeout<=0 falls back to the parent context (no added per-dial deadline).
	chk = checkerFunc(func(ctx context.Context, _ string) (bool, error) {
		_, sawDeadline = ctx.Deadline()
		return true, nil
	})
	verifyImages(t.Context(), chk, images, 0, discardLog())
	if sawDeadline {
		t.Error("timeout=0 must dial with the parent context (no added deadline)")
	}
}
