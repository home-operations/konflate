package engine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/flate/pkg/source"
)

// TestCacheGCInterval pins the sweep cadence: a fraction of the TTL clamped to
// [1h, 6h] so a long TTL still sweeps a few times a day and a short one never
// busy-loops.
func TestCacheGCInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ttl  time.Duration
		want time.Duration
	}{
		{168 * time.Hour, 6 * time.Hour}, // default 7d: 21h → clamped to the 6h ceiling
		{48 * time.Hour, 6 * time.Hour},  // 6h (48/8): exactly the ceiling
		{24 * time.Hour, 3 * time.Hour},  // 3h (24/8): within range
		{4 * time.Hour, time.Hour},       // 30m (4/8) → clamped up to the 1h floor
		{time.Minute, time.Hour},         // tiny TTL → 1h floor, never sub-hourly
	}
	for _, c := range cases {
		if got := cacheGCInterval(c.ttl); got != c.want {
			t.Errorf("cacheGCInterval(%s) = %s, want %s", c.ttl, got, c.want)
		}
	}
}

// TestSweepCacheGC_SweepsAtStartup verifies the first sweep runs immediately, not
// a full interval later — so a pod that restarts more often than the interval
// still reclaims the cache. The interval is huge so no tick can fire during the
// test; any sweep observed is therefore the startup one.
func TestSweepCacheGC_SweepsAtStartup(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	swept := make(chan struct{}, 1)
	sweep := func() {
		calls.Add(1)
		select {
		case swept <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		sweepCacheGC(ctx, sweep, time.Hour)
		close(done)
	}()

	select {
	case <-swept:
	case <-time.After(2 * time.Second):
		t.Fatal("no sweep at startup — the first sweep waited for a tick")
	}
	cancel()
	<-done
	if n := calls.Load(); n != 1 {
		t.Fatalf("sweep ran %d times, want exactly 1 (startup only; no tick at a 1h interval)", n)
	}
}

// TestLogSweep_SurfacesErrorsWithoutRemovals verifies a sweep that removed nothing
// but hit per-entry errors is still logged, not silently dropped.
func TestLogSweep_SurfacesErrorsWithoutRemovals(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logSweep(log, source.SweepResult{Errors: []error{errors.New("permission denied")}}, nil)
	if out := buf.String(); !strings.Contains(out, "completed with errors") {
		t.Errorf("a sweep with errors but no removals must be logged; got %q", out)
	}
}
