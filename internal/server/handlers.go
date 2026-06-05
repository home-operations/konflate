package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/webhook"
)

// maxWebhookBody caps an inbound webhook payload to guard against memory abuse.
const maxWebhookBody = 1 << 20 // 1 MiB

// JSON response keys.
const (
	keyError  = "error"
	keyStatus = "status"
)

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// handleDisabled is mounted on an inbound endpoint whose secret is not
// configured, so it is turned off.
func handleDisabled(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		keyError: "endpoint disabled: konflate is running without the required secret",
	})
}

// handleMeta serves the instance's non-secret identity (forge + repo + refresh
// cadence). No token or secret is included — safe even when konflate is public.
func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Meta{
		Forge:                  string(s.cfg.Forge.Kind),
		Repo:                   s.cfg.Forge.RepoPath,
		RefreshIntervalSeconds: int(s.cfg.RefreshInterval.Seconds()),
	})
}

func (s *Server) handleListPRs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.list())
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{keyError: "invalid PR number"})
		return
	}
	env, ok := s.store.get(number)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{keyError: "unknown PR"})
		return
	}
	// Ready and error are terminal (200); pending/running tell the UI to wait.
	code := http.StatusOK
	if env.Status == api.JobPending || env.Status == api.JobRunning {
		code = http.StatusAccepted
	}
	writeJSON(w, code, env)
}

// handlePush is the authenticated CI trigger to re-render a single PR. Guarded
// by a bearer token compared in constant time. Only mounted when push is
// enabled (authenticated mode + push token set).
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if !s.authorizedPush(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{keyError: "unauthorized"})
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{keyError: "invalid PR number"})
		return
	}
	if err := s.refreshPR(r.Context(), number); err != nil {
		s.log.Warn("push: fetch PR failed", "pr", number, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "could not fetch PR from forge"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{keyStatus: "accepted"})
}

// refreshPR fetches a single PR from the forge and enqueues its diff. Shared by
// the push endpoint (synchronous, surfaces fetch errors) and the webhook
// (fire-and-forget).
func (s *Server) refreshPR(ctx context.Context, number int) error {
	pr, err := s.prov.GetPR(ctx, number)
	if err != nil {
		return err
	}
	s.store.upsertPR(pr)
	s.queue.enqueue(pr)
	return nil
}

// handleWebhook verifies an inbound forge webhook and re-renders the affected
// PR (or re-lists when the open-PR set may have changed). Only mounted when
// webhooks are enabled (authenticated mode + secret).
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{keyError: "payload too large"})
		return
	}
	if err := webhook.Verify(s.cfg.Forge.Kind, s.cfg.WebhookSecret, r.Header, body); err != nil {
		s.log.Warn("webhook verification failed", "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{keyError: "signature verification failed"})
		return
	}

	// Re-render only the affected PR for content events; re-list when the
	// open-PR set may have changed (opened/closed/...) or the payload is opaque.
	if ev := webhook.Parse(s.cfg.Forge.Kind, r.Header, body); ev.PR > 0 && !ev.Relist {
		go func() {
			if err := s.refreshPR(s.runCtx, ev.PR); err != nil {
				s.log.Warn("webhook: refresh PR failed", "pr", ev.PR, "error", err)
			}
		}()
	} else {
		go s.refreshList(s.runCtx)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{keyStatus: "accepted"})
}

func (s *Server) authorizedPush(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := s.cfg.PushToken
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// refreshList re-lists open PRs from the forge: it renders newly discovered PRs
// and any whose head advanced, refreshes metadata on the rest (leaving their
// re-render to the staleness backstop in refreshLoop), and reconciles PRs that
// have left the open set. Runs at startup and once per RefreshInterval; a
// verified webhook also calls it when the open-PR set may have changed.
func (s *Server) refreshList(ctx context.Context) {
	prs, err := s.prov.ListPRs(ctx)
	if err != nil {
		s.log.Error("refresh: list PRs failed", "error", err)
		return
	}
	s.metrics.prsKnown.Set(float64(len(prs)))
	open := make(map[int]bool, len(prs))
	for _, pr := range prs {
		open[pr.Number] = true
		prev, known := s.store.get(pr.Number)
		s.store.upsertPR(pr)
		if !known || prev.PR.HeadSHA != pr.HeadSHA {
			s.queue.enqueue(pr) // new PR, or its head advanced → (re)render now
		}
	}
	s.reconcileClosed(ctx, open)
	s.log.Info("refresh listed", "prs", len(prs))
}

// reconcileClosed handles PRs that have left the forge's open set. Each is
// classified via the forge: a merged PR is frozen onto the "recently merged"
// shelf (its last rendered diff is kept and it is never re-enqueued), while an
// abandoned (closed-unmerged) PR is dropped immediately. The shelf is then
// trimmed to the retention bounds (KONFLATE_CLOSED_PR_MAX / _TTL). Every removal
// is broadcast so connected clients drop the PR live without a reload. open is
// the set of currently-open PR numbers from the just-completed list.
func (s *Server) reconcileClosed(ctx context.Context, open map[int]bool) {
	for _, number := range s.store.activeNumbers() {
		if open[number] {
			continue
		}
		pr, err := s.prov.GetPR(ctx, number)
		if err != nil {
			s.log.Warn("refresh: classify closed PR failed", "pr", number, "error", err)
			continue // leave as-is; retry on the next refresh
		}
		switch {
		case pr.Open:
			// Still open — the list momentarily missed it (pagination/flake);
			// keep it in the open group rather than dropping it.
			s.store.upsertPR(pr)
		case pr.Merged:
			s.store.markClosed(number, s.store.now())
		default:
			s.store.remove(number)
			s.hub.broadcast(api.Event{Type: eventTypeRemoved, Number: number})
		}
	}
	for _, number := range s.store.pruneClosed(s.store.now(), s.cfg.ClosedRetention, s.cfg.ClosedRetentionMax) {
		s.hub.broadcast(api.Event{Type: eventTypeRemoved, Number: number})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
