package server

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/home-operations/konflate/internal/api"
)

// gaugeValue reads a gauge's current value via the canonical Metric.Write
// interface (no extra test dependency just to assert one number).
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

// TestClassifyListResult pins how a PR-list outcome becomes a UI sync status: a
// nil error is healthy; a recognized rate limit carries its reset time so the
// banner can count down; any other error is a generic, untimed failure. A failed
// status always carries a human message for the banner.
func TestClassifyListResult(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		err        error
		wantOK     bool
		wantReason api.SyncReason
		wantRetry  int64
	}{
		{"healthy", nil, true, "", 0},
		{
			"rate limit carries the reset",
			&github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}, Message: "exceeded"},
			false, api.SyncRateLimited, reset.Unix(),
		},
		{
			"rate limit without a reset has no countdown",
			&github.RateLimitError{Message: "exceeded"},
			false, api.SyncRateLimited, 0,
		},
		{"any other error is generic", errors.New("connection refused"), false, api.SyncError, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := classifyListResult(tc.err)
			if st.OK != tc.wantOK || st.Reason != tc.wantReason || st.RetryAt != tc.wantRetry {
				t.Errorf("classifyListResult(%v) = %+v; want ok=%v reason=%q retryAt=%d",
					tc.err, st, tc.wantOK, tc.wantReason, tc.wantRetry)
			}
			if !tc.wantOK && st.Message == "" {
				t.Error("a failed sync status must carry a banner message")
			}
		})
	}
}

// TestSyncTracker_SetReportsTransitions verifies set() reports a change only when
// the health actually flips (its OK or reason) — the signal noteSyncResult uses to
// broadcast a "sync" event once per transition rather than on every refresh. A
// later reset time alone is not a transition, so a steady rate limit doesn't spam.
func TestSyncTracker_SetReportsTransitions(t *testing.T) {
	t.Parallel()
	tr := newSyncTracker()
	if !tr.status().OK {
		t.Fatal("a fresh tracker must start healthy (no banner before the first poll)")
	}
	steps := []struct {
		st          api.SyncStatus
		wantChanged bool
	}{
		{api.SyncStatus{OK: false, Reason: api.SyncRateLimited}, true},               // healthy → limited
		{api.SyncStatus{OK: false, Reason: api.SyncRateLimited, RetryAt: 99}, false}, // same health, later reset → no re-broadcast
		{api.SyncStatus{OK: false, Reason: api.SyncError}, true},                     // reason flipped
		{api.SyncStatus{OK: true}, true},                                             // recovered
		{api.SyncStatus{OK: true}, false},                                            // still healthy
	}
	for i, step := range steps {
		if got := tr.set(step.st); got != step.wantChanged {
			t.Errorf("step %d set(%+v) changed = %v, want %v", i, step.st, got, step.wantChanged)
		}
	}
}

// TestServer_SyncStatusSurfacing is the integration: a rate-limited ListPRs must
// surface on /api/meta (so a fresh page load shows the banner, not a misleading
// empty list) and flip the rate-limit metrics, and a later healthy list must clear
// both. The failing refresh must not drop PRs already loaded.
func TestServer_SyncStatusSurfacing(t *testing.T) {
	t.Parallel()
	reset := time.Now().Add(11 * time.Minute)
	prov := &fakeProvider{listErr: &github.RateLimitError{
		Rate:    github.Rate{Reset: github.Timestamp{Time: reset}},
		Message: "API rate limit exceeded",
	}}
	s := newTestServer(t, ghCfg("tok"), prov, okEngine())
	h := s.mainHandler()

	s.refreshList(s.runCtx)

	var limited api.Meta
	mustJSON(t, do(h, "GET", "/api/meta", nil, nil), &limited)
	if limited.Sync == nil || limited.Sync.OK ||
		limited.Sync.Reason != api.SyncRateLimited || limited.Sync.RetryAt != reset.Unix() {
		t.Fatalf("meta sync = %+v, want ok=false reason=rate_limited retryAt=%d", limited.Sync, reset.Unix())
	}
	if got := gaugeValue(t, s.metrics.rateLimited); got != 1 {
		t.Errorf("forge_rate_limited = %v, want 1", got)
	}
	if got := gaugeValue(t, s.metrics.rateLimitReset); got != float64(reset.Unix()) {
		t.Errorf("forge_rate_limit_reset = %v, want %d", got, reset.Unix())
	}

	// The forge recovers: once the cooldown has passed, the next list succeeds and
	// clears the banner and gauges. The rate-limit circuit breaker holds the poll
	// during the cooldown, so advance the store clock past the reset before re-polling.
	prov.mu.Lock()
	prov.listErr = nil
	prov.prs = []api.PR{{Number: 7, Open: true, HeadRef: "a", BaseRef: "main"}}
	prov.mu.Unlock()
	s.store.now = func() time.Time { return reset.Add(time.Minute) }
	s.refreshList(s.runCtx)

	// Decode into a fresh value: sync is omitempty, and Unmarshal won't clear a
	// reused struct's stale pointer.
	var healthy api.Meta
	mustJSON(t, do(h, "GET", "/api/meta", nil, nil), &healthy)
	if healthy.Sync != nil {
		t.Errorf("after recovery meta sync = %+v, want nil (healthy ⇒ omitted)", healthy.Sync)
	}
	if got := gaugeValue(t, s.metrics.rateLimited); got != 0 {
		t.Errorf("forge_rate_limited = %v, want 0 after recovery", got)
	}
	if got := gaugeValue(t, s.metrics.rateLimitReset); got != 0 {
		t.Errorf("forge_rate_limit_reset = %v, want 0 after recovery (no stale timestamp)", got)
	}
}
