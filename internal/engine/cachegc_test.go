package engine

import (
	"testing"
	"time"
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
