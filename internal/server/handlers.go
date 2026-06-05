package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	list := s.store.list()
	for i := range list {
		list[i].AuthorAvatar = s.avatarProxyPath(list[i].AuthorAvatar)
		list[i].MergeCommand = s.mergeCommand(list[i].PR)
	}
	writeJSON(w, http.StatusOK, list)
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
	env.PR.AuthorAvatar = s.avatarProxyPath(env.PR.AuthorAvatar)
	env.MergeCommand = s.mergeCommand(env.PR)
	// Ready and error are terminal (200); pending/running tell the UI to wait.
	code := http.StatusOK
	if env.Status == api.JobPending || env.Status == api.JobRunning {
		code = http.StatusAccepted
	}
	writeJSON(w, code, env)
}

// avatarClient fetches author avatars with a tight timeout; responses are
// size-capped and must be images (see handleAvatar).
var avatarClient = &http.Client{Timeout: 8 * time.Second}

const maxAvatarBytes = 2 << 20 // 2 MiB

// avatarProxyPath rewrites a raw forge avatar URL into a signed, same-origin
// /api/avatar path. The HMAC (a per-process key) means handleAvatar will only
// fetch URLs konflate itself emitted, so the proxy can't be turned into an open
// SSRF relay. Empty in, empty out.
func (s *Server) avatarProxyPath(raw string) string {
	if raw == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.avatarKey)
	mac.Write([]byte(raw))
	return "/api/avatar?u=" + url.QueryEscape(raw) + "&s=" + hex.EncodeToString(mac.Sum(nil))
}

// handleAvatar proxies an author avatar so the browser loads it same-origin (the
// CSP is img-src 'self'). Only URLs signed by avatarProxyPath are honored — the
// HMAC check keeps this safe to expose publicly. Any failure returns an error
// status, which the UI treats as "no avatar" and falls back to the person icon.
func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	mac := hmac.New(sha256.New, s.avatarKey)
	mac.Write([]byte(raw))
	got, err := hex.DecodeString(r.URL.Query().Get("s"))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		writeJSON(w, http.StatusForbidden, map[string]string{keyError: "invalid avatar signature"})
		return
	}
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" {
		writeJSON(w, http.StatusBadRequest, map[string]string{keyError: "avatar must be an https URL"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "avatar fetch failed"})
		return
	}
	resp, err := avatarClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "avatar fetch failed"})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") {
		writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "avatar unavailable"})
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxAvatarBytes))
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
	if err := s.refreshPR(r.Context(), number, "push"); err != nil {
		s.log.Warn("push: fetch PR failed", "pr", number, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{keyError: "could not fetch PR from forge"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{keyStatus: "accepted"})
}

// refreshPR fetches a single PR from the forge and enqueues its diff. Shared by
// the push endpoint (synchronous, surfaces fetch errors) and the webhook
// (fire-and-forget).
func (s *Server) refreshPR(ctx context.Context, number int, reason string) error {
	pr, err := s.prov.GetPR(ctx, number)
	if err != nil {
		return err
	}
	if !pr.Open {
		// Merged/closed since we last saw it — reconcile it onto the shelf or drop
		// it rather than enqueueing a render whose head branch may already be gone.
		s.reconcileState(pr)
		return nil
	}
	s.store.upsertPR(pr)
	s.log.Info("queuing render", "pr", number, "reason", reason)
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
			if err := s.refreshPR(s.runCtx, ev.PR, "webhook"); err != nil {
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
			reason := "head advanced"
			if !known {
				reason = "new PR"
			}
			s.log.Info("queuing render", "pr", pr.Number, "reason", reason)
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
		s.reconcileState(pr)
	}
	for _, number := range s.store.pruneClosed(s.store.now(), s.cfg.ClosedRetention, s.cfg.ClosedRetentionMax) {
		s.hub.broadcast(api.Event{Type: eventTypeRemoved, Number: number})
	}
}

// reconcileState applies a freshly-fetched PR's forge state to the store: a PR
// found open is kept in the open group (the list momentarily missed it —
// pagination/flake — or it was reopened), a merged PR is frozen onto the
// "recently merged" shelf keeping its last rendered diff, and an abandoned
// (closed-unmerged) PR is dropped and broadcast so clients remove it live.
func (s *Server) reconcileState(pr api.PR) {
	switch {
	case pr.Open:
		s.store.upsertPR(pr)
	case pr.Merged:
		s.store.markClosed(pr.Number, s.store.now())
	default:
		s.store.remove(pr.Number)
		s.hub.broadcast(api.Event{Type: eventTypeRemoved, Number: pr.Number})
	}
}

// reconcileHeadGone is called by the queue when a render finds the PR's head
// branch gone (merged/closed mid-render). It re-fetches the PR and reconciles
// its state, so instead of a spurious render failure the PR lands on the merged
// shelf (or is dropped). Best effort — a forge error just leaves it for the next
// periodic refresh to reconcile.
func (s *Server) reconcileHeadGone(number int) {
	pr, err := s.prov.GetPR(s.runCtx, number)
	if err != nil {
		s.log.Warn("reconcile head-gone PR failed", "pr", number, "error", err)
		return
	}
	s.reconcileState(pr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
