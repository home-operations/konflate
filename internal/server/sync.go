package server

import (
	"sync"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/provider"
)

// syncTracker holds the latest forge-polling health (see [api.SyncStatus]): did
// the most recent attempt to list PRs from the forge succeed? It's read by
// /api/meta for initial load and updated after every list attempt; a health
// change is pushed as a "sync" websocket event so the UI raises or clears its
// banner live. Starts healthy so no banner shows before the first attempt.
type syncTracker struct {
	mu  sync.Mutex
	cur api.SyncStatus
}

func newSyncTracker() *syncTracker {
	return &syncTracker{cur: api.SyncStatus{OK: true}}
}

func (t *syncTracker) status() api.SyncStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur
}

// set stores st and reports whether the health changed (its OK or Reason), so the
// caller broadcasts a "sync" event only on a transition rather than every refresh.
func (t *syncTracker) set(st api.SyncStatus) (changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed = t.cur.OK != st.OK || t.cur.Reason != st.Reason
	t.cur = st
	return changed
}

// classifyListResult maps a PR-list outcome to a SyncStatus: healthy on nil; a
// time-bounded "rate_limited" when the forge says so (see [provider.RateLimit]),
// raising the API rate limit is the fix); otherwise a generic "error".
func classifyListResult(err error) api.SyncStatus {
	if err == nil {
		return api.SyncStatus{OK: true}
	}
	if reset, ok := provider.RateLimit(err); ok {
		st := api.SyncStatus{OK: false, Reason: api.SyncRateLimited, Message: "The forge API rate limit was exceeded."}
		if !reset.IsZero() {
			st.RetryAt = reset.Unix()
		}
		return st
	}
	return api.SyncStatus{OK: false, Reason: api.SyncError, Message: "Couldn't reach the forge to list pull requests."}
}

// noteSyncResult records the outcome of a forge PR-list attempt: it updates the
// rate-limit metrics and the tracker, and broadcasts a "sync" event on a health
// transition so connected UIs raise or clear the banner without a reload.
func (s *Server) noteSyncResult(err error) {
	st := classifyListResult(err)

	// Derive both rate-limit gauges from the current status on every call, so a
	// recovery (or a non-rate-limit error) clears them rather than leaving a stale
	// reset timestamp behind.
	if st.Reason == api.SyncRateLimited {
		s.metrics.rateLimited.Set(1)
		s.metrics.rateLimitReset.Set(float64(st.RetryAt)) // 0 when the forge gave no reset time
	} else {
		s.metrics.rateLimited.Set(0)
		s.metrics.rateLimitReset.Set(0)
	}
	if !st.OK {
		s.metrics.listErrors.WithLabelValues(string(st.Reason)).Inc()
	}

	if s.sync.set(st) {
		s.hub.broadcast(api.Event{Type: "sync", Sync: &st})
	}
}

// metaSync is the SyncStatus for /api/meta: nil (omitted) when healthy so the UI
// shows no banner, the failed status otherwise.
func (s *Server) metaSync() *api.SyncStatus {
	if st := s.sync.status(); !st.OK {
		return &st
	}
	return nil
}
