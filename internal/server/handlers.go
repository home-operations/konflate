package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/provider"
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
	writeError(w, http.StatusNotImplemented, "endpoint disabled: konflate is running without the required secret")
}

// handleMeta serves the instance's non-secret identity (forge + repo + refresh
// cadence). No token or secret is included — safe even when konflate is public.
func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Meta{
		Forge: string(s.cfg.Forge.Kind),
		Repo:  s.cfg.Forge.RepoPath,
		// The HTTPS clone URL doubles as the repo's web page on all three forges.
		RepoURL:                s.cfg.Forge.CloneURL(),
		Version:                s.Version,
		RefreshIntervalSeconds: int(s.cfg.RefreshInterval.Seconds()),
	})
}

func init() {
	// Go's built-in MIME table has no .webmanifest entry, so the embedded PWA
	// manifest would be served with a sniffed type. Register the conventional
	// one (browsers tolerate others but warn).
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// uiHandler serves the embedded UI with cache headers tuned for content-hashed
// assets. Fingerprinted files under /assets/ are immutable and cached for a
// year — a new build changes their filename, so a stale one is never requested.
// Everything else (index.html, the favicon) is no-cache so it always
// revalidates and a redeploy is picked up immediately, even behind a CDN that
// caches by extension (e.g. Cloudflare): the hashed URLs bust the edge cache on
// their own, and the entry point is never served stale.
func (s *Server) uiHandler() http.Handler {
	fileServer := http.FileServerFS(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
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
		writeError(w, http.StatusBadRequest, "invalid PR number")
		return
	}
	env, ok := s.store.get(number)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown PR")
		return
	}
	env.PR.AuthorAvatar = s.avatarProxyPath(env.PR.AuthorAvatar)
	env.MergeCommand = s.mergeCommand(env.PR)
	// Ready and error are terminal (200); pending/running tell the UI to wait.
	code := http.StatusOK
	if env.Status == api.JobPending || env.Status == api.JobRunning {
		code = http.StatusAccepted
	}
	// A rendered diff is immutable until the next render, so serve a validator:
	// the SPA refetches the full diff on every "ready" event and the staleness
	// backstop re-renders open PRs ~every interval even when nothing changed, so
	// without this each of those repaints re-marshals the multi-MB body. With an
	// ETag they collapse to a 304 — no marshal, no transfer. Only for a ready 200
	// carrying a diff; pending/error responses are transient or bodyless.
	if code == http.StatusOK {
		if etag := s.diffETag(env); etag != "" {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "no-cache") // cacheable, but revalidate every use
			if ifNoneMatch(r, etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	writeJSON(w, code, env)
}

// diffETag derives a strong validator for a /diff response that carries a
// rendered diff. It hashes the response's light fields (the whole envelope minus
// the heavy Diff body) together with the diff's precomputed content digest
// (env.Digest, the store's savedDigest), so the ETag changes whenever any part
// of the response would change — without re-marshaling the multi-MB body. The
// signed avatar path is part of the light fields, so a restart (which re-signs
// it with a fresh key) yields a different ETag, never a stale 304. Returns ""
// when there is no rendered diff to validate.
func (s *Server) diffETag(env api.DiffEnvelope) string {
	if env.Diff == nil {
		return ""
	}
	light := env
	light.Diff = nil // env.Digest already stands in for the body
	meta, err := json.Marshal(light)
	if err != nil {
		return "" // never expected; fall back to an unconditional response
	}
	h := fnv.New64a()
	var d [8]byte
	binary.LittleEndian.PutUint64(d[:], env.Digest)
	_, _ = h.Write(d[:])
	_, _ = h.Write(meta)
	return `"` + strconv.FormatUint(h.Sum64(), 16) + `"`
}

// ifNoneMatch reports whether the request's If-None-Match matches etag — an
// exact strong-validator match, or "*". Per RFC 9110 the header is a
// comma-separated list; konflate only emits strong ETags, so a strong compare is
// correct.
func ifNoneMatch(r *http.Request, etag string) bool {
	inm := r.Header.Get("If-None-Match")
	if inm == "" {
		return false
	}
	if strings.TrimSpace(inm) == "*" {
		return true
	}
	for _, candidate := range strings.Split(inm, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

// handleSummary serves the same envelope as handleDiff but with the heavy
// per-resource render dropped (Resources, Tree, ChromaCSS). It backs the PR
// list's row expander, which only needs the headline facts (impact, cautions,
// image bumps, failures) and shouldn't pull the full diff payload to show them.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid PR number")
		return
	}
	env, ok := s.store.get(number)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown PR")
		return
	}
	env.PR.AuthorAvatar = s.avatarProxyPath(env.PR.AuthorAvatar)
	env.MergeCommand = s.mergeCommand(env.PR)
	env.ReviewURL = reviewURLFromRequest(r, number)
	if env.Diff != nil {
		// Shallow-copy then trim, so the cached diff keeps its rendered resources —
		// only this response is lightened.
		lite := *env.Diff
		lite.Resources = nil
		lite.Tree = nil
		lite.ChromaCSS = ""
		env.Diff = &lite
	}
	rendering := env.Status == api.JobPending || env.Status == api.JobRunning
	// A render-status header (ok / failures / error / pending) lets a CI gate
	// decide pass/fail from the very request it fetches the comment body with — no
	// second JSON call. Set before either branch writes its status line.
	w.Header().Set(renderStatusHeader, renderStatus(env))
	// Content negotiation: a caller asking for Markdown gets a paste-ready block
	// (forge flavour from ?forge=, defaulting to this instance's forge — so a
	// GitHub-watching konflate emits [!CAUTION] admonitions with no param). Every
	// other caller, the SPA included, gets the JSON envelope unchanged.
	if strings.Contains(r.Header.Get("Accept"), "text/markdown") {
		flavor := r.URL.Query().Get("forge")
		if flavor == "" {
			flavor = string(s.cfg.Forge.Kind)
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		// A Markdown consumer (a CI comment poster) waits with `curl --retry`. While
		// the render is in flight, answer 503 + Retry-After so the retry kicks in —
		// 202 is a success curl wouldn't retry. The JSON path keeps 202 (the SPA
		// treats it as "still loading", not an error).
		if rendering {
			w.Header().Set("Retry-After", strconv.Itoa(summaryRetryAfterSeconds))
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = io.WriteString(w, summaryMarkdown(env, env.ReviewURL, flavor == "github"))
		return
	}
	code := http.StatusOK
	if rendering {
		code = http.StatusAccepted
	}
	writeJSON(w, code, env)
}

// summaryRetryAfterSeconds is the Retry-After hint on a still-rendering Markdown
// summary response, so a `curl --retry` consumer backs off a beat between tries.
const summaryRetryAfterSeconds = 3

// renderStatusHeader names the response header carrying the render verdict on
// the summary endpoint, so a CI gate can pass/fail off one request. Values are
// from [renderStatus].
const renderStatusHeader = "X-Konflate-Render-Status"

// renderStatus reduces a PR's envelope to the verdict a CI gate cares about:
//
//	ok       — rendered cleanly, no resource failed
//	failures — rendered, but flate could not render one or more resources
//	error    — the render itself errored (Error is set, no diff)
//	pending  — still queued/rendering (the Markdown path 503s here, so a
//	           `curl --retry` consumer only observes a terminal value)
//
// "failures" and "error" are the fail-the-PR cases; a transient RefreshError
// (last-good diff still shown) is deliberately reported as "ok".
func renderStatus(env api.DiffEnvelope) string {
	switch env.Status {
	case api.JobError:
		return "error"
	case api.JobReady:
		if env.Diff != nil && len(env.Diff.Failures) > 0 {
			return "failures"
		}
		return "ok"
	default: // pending, running
		return "pending"
	}
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
		writeError(w, http.StatusForbidden, "invalid avatar signature")
		return
	}
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" {
		writeError(w, http.StatusBadRequest, "avatar must be an https URL")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "avatar fetch failed")
		return
	}
	resp, err := avatarClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "avatar fetch failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") {
		writeError(w, http.StatusBadGateway, "avatar unavailable")
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
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid PR number")
		return
	}
	if err := s.refreshPR(r.Context(), number, "push"); err != nil {
		s.log.Warn("push: fetch PR failed", "pr", number, "error", err)
		writeError(w, http.StatusBadGateway, "could not fetch PR from forge")
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
	if !pr.Open || !s.prAllowed(pr) {
		// Merged/closed since we last saw it, or filtered out by the PR filter —
		// reconcile (shelve or drop) instead of enqueueing a render whose head
		// branch may already be gone, or that we shouldn't track.
		s.reconcileState(pr)
		return nil
	}
	s.store.upsertPR(pr, false)
	s.log.Info("queuing render", "pr", number, "reason", reason)
	s.queue.enqueue(pr)
	return nil
}

// prAllowed reports whether a PR should be rendered — the render decision used
// everywhere live. It collapses prVerdict's (allowed, error) result: a filter
// evaluation error hides the PR (and logs). dropFilteredOnLoad is the one caller
// that must tell an eval error apart from an intentional exclusion, so it uses
// prVerdict directly (an error there must never delete persisted state).
func (s *Server) prAllowed(pr api.PR) bool {
	allowed, err := s.prVerdict(pr)
	if err != nil {
		s.log.Error("PR filter evaluation failed; hiding PR",
			"pr", pr.Number, "expr", s.cfg.PRFilter.Source(), "error", err)
		return false
	}
	return allowed
}

// prVerdict decides whether pr should be rendered, separating an intentional
// exclusion (allowed=false, err=nil) from a filter evaluation error (err!=nil).
// Two gates are AND-ed for the verdict: the fork gate (KONFLATE_RENDER_FORK_PRS,
// default off) excludes forks regardless of the expression — so editing the
// filter can't accidentally enable untrusted fork rendering — and the CEL filter
// (KONFLATE_PR_FILTER_EXPR) decides the rest. A PR that fails either is still
// tracked, but hidden and never rendered. A nil filter admits every non-fork PR;
// that only happens when a Config is built without Load (i.e. in tests). The
// error is non-nil only when the expression fails to evaluate (e.g. it
// references a field the PR map doesn't carry); checkFilter turns that class of
// mistake into a fail-fast startup error.
func (s *Server) prVerdict(pr api.PR) (allowed bool, err error) {
	if pr.Fork && !s.cfg.RenderForkPRs {
		return false, nil // untrusted fork code: never rendered unless explicitly enabled
	}
	if s.cfg.PRFilter == nil {
		return true, nil
	}
	return s.cfg.PRFilter.Eval(prFilterVars(pr))
}

// prFilterVars projects a PR into the `pr` variable the CEL filter evaluates
// against (see config.Config.PRFilterExpr for the documented field set). Every
// field is always present, so a valid field reference never hits a missing key;
// labels are [{name, color}] for `pr.labels.exists(l, l.name == "…")`.
func prFilterVars(pr api.PR) map[string]any {
	labels := make([]any, len(pr.Labels))
	for i, l := range pr.Labels {
		labels[i] = map[string]any{"name": l.Name, "color": l.Color}
	}
	return map[string]any{
		"number":    pr.Number,
		"title":     pr.Title,
		"author":    pr.Author,
		"state":     pr.State,
		"open":      pr.Open,
		"merged":    pr.Merged,
		"draft":     pr.Draft,
		"fork":      pr.Fork,
		"headRef":   pr.HeadRef,
		"headSha":   pr.HeadSHA,
		"baseRef":   pr.BaseRef,
		"url":       pr.URL,
		"createdAt": pr.CreatedAt,
		"labels":    labels,
	}
}

// handleWebhook verifies an inbound forge webhook and re-renders the affected
// PR (or re-lists when the open-PR set may have changed). Only mounted when
// webhooks are enabled (authenticated mode + secret).
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	if err := webhook.Verify(s.cfg.Forge.Kind, s.cfg.WebhookSecret, r.Header, body); err != nil {
		s.log.Warn("webhook verification failed", "error", err)
		writeError(w, http.StatusUnauthorized, "signature verification failed")
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
		// Coalesce: a chatty webhook (every check_run/status/push delivered) would
		// otherwise spawn a goroutine + full ListPRs per event. requestRelist folds
		// the burst into at most one in-flight relist plus one trailing run.
		s.requestRelist()
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
	open := make(map[int]struct{}, len(prs))
	var added, advanced, unhidden int
	for _, pr := range prs {
		// Every open PR is tracked. One the filter excludes is kept as hidden —
		// listed (greyed, under the "hidden" pill) but never enqueued, so a fork's
		// untrusted code is never rendered.
		allowed := s.prAllowed(pr)
		open[pr.Number] = struct{}{}
		prev, known := s.store.get(pr.Number)
		s.store.upsertPR(pr, !allowed)
		// Render a PR that is newly tracked, whose head advanced, or that just
		// became filter-allowed without a push (e.g. draft → ready under a
		// !pr.draft filter). That last case is easy to miss: in polling mode
		// nothing else enqueues it, and the staleness backstop skips never-rendered
		// jobs, so it would sit pending forever.
		nowAllowed := known && prev.Hidden && allowed
		if allowed && (!known || prev.PR.HeadSHA != pr.HeadSHA || nowAllowed) {
			reason := "head advanced"
			switch {
			case !known:
				reason = "new PR"
				added++
			case nowAllowed:
				reason = "filter now admits"
				unhidden++
			default:
				advanced++
			}
			s.log.Debug("queuing render", "pr", pr.Number, "reason", reason)
			s.queue.enqueue(pr)
		}
	}
	s.reconcileClosed(ctx, open)
	// One summary line per refresh, with counts, instead of one "queuing render"
	// line per PR: at startup every PR is "new", which would be a burst of
	// near-identical lines on a busy instance. The per-PR detail stays at debug.
	s.log.Info("refresh listed", "prs", len(prs), "new", added, "advanced", advanced, "unhidden", unhidden)
}

// reconcileClosed handles PRs that have left the forge's open set. Each is
// classified via the forge: a merged PR is frozen onto the "recently merged"
// shelf (its last rendered diff is kept and it is never re-enqueued), while an
// abandoned (closed-unmerged) PR is dropped immediately. The shelf is then
// trimmed to the retention bounds (KONFLATE_CLOSED_PR_MAX / _TTL). Every removal
// is broadcast so connected clients drop the PR live without a reload. open is
// the set of currently-open PR numbers from the just-completed list.
func (s *Server) reconcileClosed(ctx context.Context, open map[int]struct{}) {
	for _, number := range s.store.activeNumbers() {
		if _, ok := open[number]; ok {
			continue
		}
		pr, err := s.prov.GetPR(ctx, number)
		if err != nil {
			if errors.Is(err, provider.ErrPRNotFound) {
				// Deleted on the forge (not merged/closed): reap it, rather than
				// 404ing on this lookup every refresh forever.
				s.dropPR(number)
				continue
			}
			s.log.Warn("refresh: classify closed PR failed", "pr", number, "error", err)
			continue // transient: leave as-is, retry on the next refresh
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
	allowed := s.prAllowed(pr)
	switch {
	case pr.Open:
		// Open is always tracked. A PR the filter excludes is kept as hidden
		// (listed, greyed, never rendered); an admitted one is tracked normally and
		// its render is driven by the caller / the staleness backstop.
		s.store.upsertPR(pr, !allowed)
	case pr.Merged && allowed:
		// Merged and still admitted: freeze its last rendered diff on the shelf.
		s.store.markClosed(pr.Number, s.store.now())
	default:
		// Abandoned, or filtered-out (a hidden PR that merged kept no diff worth
		// shelving): drop it and tell clients to remove it.
		s.dropPR(pr.Number)
	}
}

// dropPR removes a PR from the store and tells connected clients to drop it.
// store.remove is a no-op for an untracked PR, so this is safe to call for a PR
// we may not be tracking (e.g. a webhook for an unrelated one).
func (s *Server) dropPR(number int) {
	s.store.remove(number)
	s.hub.broadcast(api.Event{Type: eventTypeRemoved, Number: number})
}

// reconcileHeadGone is called by the queue when a render finds the PR's head
// branch gone (merged/closed mid-render). It re-fetches the PR and reconciles
// its state, so instead of a spurious render failure the PR lands on the merged
// shelf (or is dropped). Best effort — a forge error just leaves it for the next
// periodic refresh to reconcile.
func (s *Server) reconcileHeadGone(number int) {
	pr, err := s.prov.GetPR(s.runCtx, number)
	if err != nil {
		if errors.Is(err, provider.ErrPRNotFound) {
			s.dropPR(number) // deleted on the forge: reap it rather than loop on the gone head ref
			return
		}
		s.log.Warn("reconcile head-gone PR failed", "pr", number, "error", err)
		return
	}
	s.reconcileState(pr)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	// These bodies are application/json with X-Content-Type-Options: nosniff, so
	// they can't be sniffed as HTML — escaping </>/& to \u00xx only bloats the
	// payload, and the diff is span-dense chroma HTML (~6x on those bytes).
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError writes a JSON error body ({"error": msg}) with the given status.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{keyError: msg})
}
